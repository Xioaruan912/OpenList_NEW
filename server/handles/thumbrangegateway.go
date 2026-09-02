package handles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	log "github.com/sirupsen/logrus"
)

const thumbRangeGatewayMaxRead = int64(32 * 1024 * 1024)

type thumbRangeGatewaySession struct {
	ctx    context.Context
	reader model.RangeReaderIF
	size   int64
	mu     sync.Mutex
}

var (
	thumbRangeGatewayOnce     sync.Once
	thumbRangeGatewayAddr     string
	thumbRangeGatewayErr      error
	thumbRangeGatewaySessions sync.Map // random token -> *thumbRangeGatewaySession
)

func startThumbRangeGateway() (string, error) {
	thumbRangeGatewayOnce.Do(func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			thumbRangeGatewayErr = err
			return
		}
		thumbRangeGatewayAddr = listener.Addr().String()
		server := &http.Server{
			Handler:           http.HandlerFunc(serveThumbRangeGateway),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
		}
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Errorf("thumbnail loopback range gateway stopped: %v", err)
			}
		}()
		log.Infof("thumbnail loopback range gateway listening on %s", thumbRangeGatewayAddr)
	})
	return thumbRangeGatewayAddr, thumbRangeGatewayErr
}

func newThumbRangeGatewayToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// registerThumbRangeGateway exposes a RangeReader-only link to ffmpeg/ffprobe through a random,
// process-local URL. The listener is bound to loopback and the token disappears with the caller's
// context or cleanup function, so this never becomes a public download endpoint.
func registerThumbRangeGateway(ctx context.Context, rawPath string, reader model.RangeReaderIF, size int64) (string, func(), error) {
	if reader == nil {
		return "", nil, errors.New("range gateway requires RangeReader")
	}
	if size <= 0 {
		return "", nil, fmt.Errorf("range gateway requires positive size, got %d", size)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	addr, err := startThumbRangeGateway()
	if err != nil {
		return "", nil, err
	}
	token, err := newThumbRangeGatewayToken()
	if err != nil {
		return "", nil, err
	}
	session := &thumbRangeGatewaySession{ctx: ctx, reader: reader, size: size}
	thumbRangeGatewaySessions.Store(token, session)
	stop := context.AfterFunc(ctx, func() { thumbRangeGatewaySessions.Delete(token) })
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			stop()
			thumbRangeGatewaySessions.Delete(token)
		})
	}
	name := url.PathEscape(stdpath.Base(rawPath))
	if name == "" || name == "." || name == "/" {
		name = "media"
	}
	return "http://" + addr + "/range/" + token + "/" + name, cleanup, nil
}

func thumbRangeGatewayToken(path string) string {
	const prefix = "/range/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	token, _, _ := strings.Cut(rest, "/")
	if len(token) != 32 {
		return ""
	}
	return token
}

func boundThumbGatewayRange(r http_range.Range, size int64) (http_range.Range, bool) {
	if size <= 0 || r.Start < 0 || r.Start >= size || r.Length <= 0 {
		return http_range.Range{}, false
	}
	if r.Length > thumbRangeGatewayMaxRead {
		r.Length = thumbRangeGatewayMaxRead
	}
	if r.Start+r.Length > size {
		r.Length = size - r.Start
	}
	return r, r.Length > 0
}

func serveThumbRangeGateway(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := thumbRangeGatewayToken(r.URL.Path)
	value, ok := thumbRangeGatewaySessions.Load(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	session := value.(*thumbRangeGatewaySession)
	thumbRuntimeMetrics.rangeGateway.Add(1)
	if err := session.ctx.Err(); err != nil {
		thumbRangeGatewaySessions.Delete(token)
		http.Error(w, "range session expired", http.StatusGone)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(session.size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	ranges, err := http_range.ParseRange(r.Header.Get("Range"), session.size)
	if err != nil || len(ranges) > 1 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", session.size))
		http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	var requested http_range.Range
	if len(ranges) == 0 {
		// libav usually asks for bytes=0-, but an initial request without Range is also valid.
		// Return a bounded 206 slice so the caller still learns the real total size and can seek.
		requested = http_range.Range{Start: 0, Length: min(session.size, thumbRangeGatewayMaxRead)}
	} else {
		requested = ranges[0]
	}
	requested, ok = boundThumbGatewayRange(requested, session.size)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", session.size))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	ctx, cancel := context.WithCancel(session.ctx)
	stopRequest := context.AfterFunc(r.Context(), cancel)
	defer func() {
		stopRequest()
		cancel()
	}()

	session.mu.Lock()
	reader, err := session.reader.RangeRead(ctx, requested)
	if err != nil {
		session.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if reader == nil {
		session.mu.Unlock()
		http.Error(w, "range reader returned nil body", http.StatusBadGateway)
		return
	}
	defer func() {
		_ = reader.Close()
		session.mu.Unlock()
	}()

	w.Header().Set("Content-Range", requested.ContentRange(session.size))
	w.Header().Set("Content-Length", strconv.FormatInt(requested.Length, 10))
	w.WriteHeader(http.StatusPartialContent)
	if _, err := io.CopyN(w, reader, requested.Length); err != nil && !errors.Is(err, context.Canceled) {
		log.Debugf("thumbnail loopback range copy ended early: %v", err)
	}
}

// thumbFFmpegSource keeps normal URL-backed drivers on their direct path. RangeReader-only drivers
// get a temporary loopback HTTP URL because ffmpeg/ffprobe cannot consume the Go RangeReader API.
// The original remote headers/proxy must not be forwarded to the loopback URL.
func thumbFFmpegSource(ctx context.Context, rawPath string, link *model.Link, size int64, proxy string) (sourceURL string, header http.Header, sourceProxy string, cleanup func(), err error) {
	cleanup = func() {}
	if link == nil {
		return "", nil, "", cleanup, errors.New("nil media link")
	}
	if link.URL != "" {
		return link.URL, link.Header, proxy, cleanup, nil
	}
	if link.RangeReader == nil {
		return "", nil, "", cleanup, errors.New("media link has neither URL nor RangeReader")
	}
	if size <= 0 && link.ContentLength > 0 {
		size = link.ContentLength
	}
	gatewayURL, gatewayCleanup, err := registerThumbRangeGateway(ctx, rawPath, link.RangeReader, size)
	if err != nil {
		return "", nil, "", cleanup, err
	}
	return gatewayURL, nil, "", gatewayCleanup, nil
}
