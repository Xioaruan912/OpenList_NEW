package handles

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	log "github.com/sirupsen/logrus"
)

// 视频时长探测：ffprobe 远程读取（只传输 moov 元数据），结果内存缓存
var (
	videoDurOnce   sync.Once
	videoDurPath   string
	videoDurErr    error
	videoDurMu     sync.Mutex
	videoDurCache  = map[string]videoDurEntry{}
	videoDurProbe  = map[string]bool{} // 探测中，避免重复
	videoDurSem    = make(chan struct{}, 4)
	videoDurProbeN int64
)

type videoDurEntry struct {
	dur float64
	at  time.Time
}

const (
	videoDurCacheTTL = 24 * time.Hour
	videoDurMissTTL  = time.Hour
)

func ffprobeBin() (string, error) {
	videoDurOnce.Do(func() {
		videoDurPath, videoDurErr = exec.LookPath("ffprobe")
		if videoDurErr != nil {
			log.Warnf("ffprobe not found, video duration disabled: %s", videoDurErr)
		}
	})
	return videoDurPath, videoDurErr
}

func clearVideoDurationProbe(rawPath string) {
	videoDurMu.Lock()
	delete(videoDurProbe, rawPath)
	videoDurMu.Unlock()
}

func videoDurationCacheKey(rawPath string) string {
	if fingerprint := thumbKnownFingerprint(thumbKindVideo, rawPath); validThumbFingerprint(fingerprint) {
		return fingerprint
	}
	return rawPath
}

func cacheVideoDurationResult(rawPath string, duration float64, ok bool) {
	videoDurMu.Lock()
	defer videoDurMu.Unlock()
	delete(videoDurProbe, rawPath)
	if ok {
		videoDurCache[rawPath] = videoDurEntry{dur: duration, at: time.Now()}
		return
	}
	videoDurCache[rawPath] = videoDurEntry{dur: 0, at: time.Now().Add(videoDurCacheTTL - videoDurMissTTL)}
}

// probeVideoDuration 同步探测视频时长（秒），探测失败返回 0。
// ffprobe 直接读取驱动返回的 URL/Header，不再经由外部请求 Host 拼接的 /d 地址。
func probeVideoDuration(ctx context.Context, rawPath string) float64 {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	if _, err := ffprobeBin(); err != nil {
		return 0
	}
	cacheKey := videoDurationCacheKey(rawPath)
	videoDurMu.Lock()
	if e, ok := videoDurCache[cacheKey]; ok {
		if time.Since(e.at) < videoDurCacheTTL {
			videoDurMu.Unlock()
			return e.dur
		}
	}
	if videoDurProbe[cacheKey] {
		videoDurMu.Unlock()
		return 0
	}
	videoDurProbe[cacheKey] = true
	videoDurMu.Unlock()

	select {
	case videoDurSem <- struct{}{}:
	case <-ctx.Done():
		clearVideoDurationProbe(cacheKey)
		return 0
	}
	defer func() { <-videoDurSem }()
	videoDurMu.Lock()
	videoDurProbeN++
	videoDurMu.Unlock()

	link, obj, err := fs.Link(ctx, rawPath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		if link != nil {
			_ = link.Close()
		}
		cacheVideoDurationResult(cacheKey, 0, false)
		return 0
	}
	defer link.Close()
	proxy := thumbProxyForPath(rawPath)
	sourceURL, sourceHeader, sourceProxy, sourceCleanup, err := thumbFFmpegSource(ctx, rawPath, link, obj.GetSize(), proxy)
	if err != nil {
		cacheVideoDurationResult(cacheKey, 0, false)
		return 0
	}
	defer sourceCleanup()
	args := []string{"-v", "error", "-rw_timeout", "20000000"}
	if headers := ffmpegHTTPHeaders(sourceHeader); headers != "" {
		args = append(args, "-headers", headers)
	}
	if sourceProxy != "" {
		args = append(args, "-http_proxy", sourceProxy)
	}
	args = append(args, "-show_entries", "format=duration", "-of", "csv=p=0", sourceURL)
	cmdCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, videoDurPath, args...)
	out, err := cmd.Output()
	if err != nil {
		cacheVideoDurationResult(cacheKey, 0, false)
		return 0
	}
	dur, parseErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if parseErr != nil || dur <= 0 {
		cacheVideoDurationResult(cacheKey, 0, false)
		return 0
	}
	cacheVideoDurationResult(cacheKey, dur, true)
	return dur
}

// videoDuration 读取缓存的视频时长（未缓存则异步探测，返回当前值）
func videoDuration(ctx context.Context, rawPath string) float64 {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	cacheKey := videoDurationCacheKey(rawPath)
	videoDurMu.Lock()
	e, ok := videoDurCache[cacheKey]
	probing := videoDurProbe[cacheKey]
	videoDurMu.Unlock()
	if ok && time.Since(e.at) < videoDurCacheTTL {
		return e.dur
	}
	if !ok && !probing {
		// 异步探测（列表场景不阻塞）
		go func() {
			probeVideoDuration(context.Background(), rawPath)
		}()
	}
	return 0
}

// videoDurationSync 同步探测（详情场景）
func videoDurationSync(ctx context.Context, rawPath string) float64 {
	return probeVideoDuration(ctx, rawPath)
}
