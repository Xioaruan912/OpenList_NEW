package handles

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	stdpath "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	"golang.org/x/sync/singleflight"
)

const (
	thumbKindVideo = "video"
	thumbKindAudio = "audio"
	thumbKindImage = "image"
	thumbKindCover = "cover"

	thumbCacheDir  = "thumb_cache"
	thumbFailTTL   = 7 * 24 * time.Hour
	thumbTmpTTL    = time.Hour
	thumbCleanupIt = time.Hour

	thumbChunkSize = 3 * 1024 * 1024
	thumbWidth     = 288

	// 长视频内容缩略图：超过阈值时取"中间单帧"
	thumbMosaicLongSec = 90
	thumbProbeMinSize  = 10 * 1024 * 1024 // 小于该大小的文件不做时长探测
)

var (
	thumbCacheOnce sync.Once
	thumbCacheRoot string

	ffmpegOnce sync.Once
	ffmpegPath string
	ffmpegErr  error

	thumbCleanupOnce sync.Once

	errThumbTooLarge   = errors.New("file too large for thumbnail")
	errThumbNoCover    = errors.New("no cover art or cover file found")
	errThumbBlank      = errors.New("blank thumbnail")
	errThumbRemoteRisk = errors.New("remote frame extraction blocked")

	thumbGenerateGroup singleflight.Group
)

func generateThumbOnce(kind, rawPath string, generate func() ([]byte, error)) ([]byte, error) {
	value, err, _ := thumbGenerateGroup.Do(kind+"\x00"+rawPath, func() (interface{}, error) {
		return generate()
	})
	if err != nil {
		return nil, err
	}
	data, ok := value.([]byte)
	if !ok {
		return nil, errors.New("thumbnail generator returned invalid data")
	}
	return data, nil
}

// isPermanentThumbError 判断生成失败是否属于"永久性"（重试无意义）：
// 115 文件级拦截（403/pmt）、下载被拒等。这类错误写失败标记、不重试，避免长时间空转。
func isPermanentThumbError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errThumbRemoteRisk) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"403", "405", "pmt", "forbidden", "access denied", "proxy authentication", "unable to extract any video frame"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// thumbFailReason 把生成错误映射为面向用户的简短失败原因
func thumbFailReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, errThumbTooLarge):
		return "文件过大"
	case errors.Is(err, errThumbNoCover):
		return "无封面/无法抽帧"
	case errors.Is(err, errThumbRemoteRisk):
		return "115 风控拦截"
	case strings.Contains(msg, "403") || strings.Contains(msg, "pmt") || strings.Contains(msg, "access denied"):
		return "115 拦截访问（无法取帧）"
	case strings.Contains(msg, "405"):
		return "115 风控拦截"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "下载/抽帧超时"
	case strings.Contains(msg, "partial file"):
		return "视频数据不完整（深偏移被限）"
	case strings.Contains(msg, "unable to extract") || strings.Contains(msg, "no frame"):
		return "无法抽帧（可能被 115 拦截或文件损坏）"
	case strings.Contains(msg, "blank"):
		return "生成结果为空白图"
	default:
		return "生成失败"
	}
}

// isBlankThumb 判断生成的缩略图是否为近纯色/空白图（抽帧失败时 ffmpeg 常输出纯白图）。
// 采用网格采样：99% 以上像素与左上角基准色相差 <=10 视为空白。
func isBlankThumb(png []byte) bool {
	img, err := imaging.Decode(bytes.NewReader(png))
	if err != nil {
		return false // 无法解码交给上层错误处理
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 8 || h < 8 {
		return true
	}
	br, bg, bb, _ := color.RGBAModel.Convert(img.At(b.Min.X, b.Min.Y)).RGBA()
	baseR, baseG, baseB := uint8(br>>8), uint8(bg>>8), uint8(bb>>8)
	diff := func(a, c uint8) int {
		d := int(a) - int(c)
		if d < 0 {
			d = -d
		}
		return d
	}
	same, total := 0, 0
	for x := b.Min.X; x < b.Max.X; x += 4 {
		for y := b.Min.Y; y < b.Max.Y; y += 4 {
			r, g, bl, _ := color.RGBAModel.Convert(img.At(x, y)).RGBA()
			if diff(uint8(r>>8), baseR) <= 10 && diff(uint8(g>>8), baseG) <= 10 && diff(uint8(bl>>8), baseB) <= 10 {
				same++
			}
			total++
		}
	}
	return total > 0 && float64(same)/float64(total) >= 0.99
}

// scoreVideoThumb 为候选帧计算轻量的内容质量分数。分数只用于推荐，
// 不会替代用户手动选择；不依赖 AI，避免为远程视频引入额外下载。
func scoreVideoThumb(png []byte) float64 {
	img, err := imaging.Decode(bytes.NewReader(png))
	if err != nil {
		return -1
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 8 || height < 8 {
		return -1
	}

	stepX, stepY := width/64, height/36
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}

	lumaAt := func(x, y int) float64 {
		r, g, b, _ := color.RGBAModel.Convert(img.At(x, y)).RGBA()
		return (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 65535
	}
	saturationAt := func(x, y int) float64 {
		r, g, b, _ := color.RGBAModel.Convert(img.At(x, y)).RGBA()
		rf, gf, bf := float64(r)/65535, float64(g)/65535, float64(b)/65535
		maxc, minc := rf, rf
		if gf > maxc {
			maxc = gf
		}
		if bf > maxc {
			maxc = bf
		}
		if gf < minc {
			minc = gf
		}
		if bf < minc {
			minc = bf
		}
		return maxc - minc
	}

	var (
		count, edgeCount int
		sum, sumSquares  float64
		edge, saturation float64
		minLuma          = 1.0
		maxLuma          float64
	)
	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			luma := lumaAt(x, y)
			sum += luma
			sumSquares += luma * luma
			saturation += saturationAt(x, y)
			if luma < minLuma {
				minLuma = luma
			}
			if luma > maxLuma {
				maxLuma = luma
			}
			count++
			if x+stepX < bounds.Max.X {
				edge += math.Abs(luma - lumaAt(x+stepX, y))
				edgeCount++
			}
			if y+stepY < bounds.Max.Y {
				edge += math.Abs(luma - lumaAt(x, y+stepY))
				edgeCount++
			}
		}
	}
	if count == 0 {
		return -1
	}

	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	contrast := math.Sqrt(variance)
	edgeMean := 0.0
	if edgeCount > 0 {
		edgeMean = edge / float64(edgeCount)
	}
	dynamicRange := maxLuma - minLuma
	score := contrast*0.5 + edgeMean*0.35 + (saturation/float64(count))*0.15
	if dynamicRange < 0.04 {
		score *= 0.2
	}
	if mean < 0.08 {
		score *= mean / 0.08
	} else if mean > 0.95 {
		score *= (1 - mean) / 0.05
	}
	if score < 0 {
		return 0
	}
	return score
}

const (
	videoContactSheetColumns    = 3
	videoContactSheetRows       = 3
	videoContactSheetCellWidth  = 96
	videoContactSheetCellHeight = 54 // 16:9，与默认 288px 缩略图保持一致
	videoContactSheetWidth      = videoContactSheetColumns * videoContactSheetCellWidth
	videoContactSheetHeight     = videoContactSheetRows * videoContactSheetCellHeight
)

// buildVideoContactSheet 将候选帧按原始位置合成 3×3 图。
// 取帧失败的位置保留深色空格，便于前端仍显示已成功的候选。
func buildVideoContactSheet(frames [][]byte) ([]byte, error) {
	sheet := imaging.New(videoContactSheetWidth, videoContactSheetHeight, color.NRGBA{R: 16, G: 16, B: 16, A: 255})
	for i := 0; i < videoContactSheetColumns*videoContactSheetRows && i < len(frames); i++ {
		if len(frames[i]) == 0 {
			continue
		}
		img, err := imaging.Decode(bytes.NewReader(frames[i]))
		if err != nil {
			continue
		}
		tile := imaging.Fill(img, videoContactSheetCellWidth, videoContactSheetCellHeight, imaging.Center, imaging.Lanczos)
		x := (i % videoContactSheetColumns) * videoContactSheetCellWidth
		y := (i / videoContactSheetColumns) * videoContactSheetCellHeight
		sheet = imaging.Paste(sheet, tile, image.Point{X: x, Y: y})
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, sheet, imaging.PNG); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// thumbSemMu 动态并发信号量：容量来自设置 thumb_concurrency
var (
	thumbSemMu    sync.Mutex
	thumbSemCount int
)

// thumbAcquire 获取生成并发名额。wait > 0 时最多等待指定时长，
// wait <= 0 时只受 ctx 控制；直接请求取消后不会继续占着 goroutine 等名额。
func thumbAcquire(ctx context.Context, wait time.Duration) (got bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return false
	default:
	}
	limit := thumbGenPower().AcquireLimit
	if limit < 1 {
		limit = 1
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if wait > 0 {
		timer = time.NewTimer(wait)
		timeout = timer.C
		defer timer.Stop()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		thumbSemMu.Lock()
		if thumbSemCount < limit {
			thumbSemCount++
			thumbSemMu.Unlock()
			return true
		}
		thumbSemMu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-timeout:
			return false
		case <-ticker.C:
		}
	}
}

func thumbRelease() {
	thumbSemMu.Lock()
	thumbSemCount--
	thumbSemMu.Unlock()
}

// 预热队列：浏览目录时后台批量生成视频缩略图
type thumbPrewarmTask struct {
	kind    string
	rawPath string
}

var (
	prewarmOnce   sync.Once
	prewarmCh     chan thumbPrewarmTask
	prewarmDone   sync.Map // path -> done
	prewarmDirDeb sync.Map // dir -> last prewarm time, 防抖
	// 队列暂停标记：暂停时 worker 不再取任务，已入队任务保留等待恢复
	thumbQueuePaused atomic.Bool
)

const prewarmDebounce = 10 * time.Minute

const thumbUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func thumbDir() string {
	thumbCacheOnce.Do(func() {
		// 缓存放数据目录根下（data/thumb_cache），不放 temp/ —— 启动时 CleanTempDir 会清空 temp
		thumbCacheRoot = filepath.Join(filepath.Dir(conf.Conf.TempDir), thumbCacheDir)
		_ = os.MkdirAll(thumbCacheRoot, 0o755)
	})
	return thumbCacheRoot
}

func thumbHash(rawPath string) string {
	h := md5.Sum([]byte(rawPath))
	return hex.EncodeToString(h[:])
}

func thumbCachePath(kind, rawPath string) string {
	return filepath.Join(thumbDir(), fmt.Sprintf("%s-%s.png", kind, thumbHash(rawPath)))
}

func thumbFailPath(kind, rawPath string) string {
	return filepath.Join(thumbDir(), fmt.Sprintf("%s-%s.fail", kind, thumbHash(rawPath)))
}

func newThumbTempPath(kind, rawPath, suffix string) (string, error) {
	f, err := os.CreateTemp(thumbDir(), fmt.Sprintf("%s-%s-*%s", kind, thumbHash(rawPath), suffix))
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func thumbFailTTLDuration() time.Duration {
	sec := setting.GetInt(conf.ThumbFailTTL, 7*24*60*60)
	if sec <= 0 {
		sec = 7 * 24 * 60 * 60
	}
	return time.Duration(sec) * time.Second
}

func thumbFailed(kind, rawPath string) bool {
	fi, err := os.Stat(thumbFailPath(kind, rawPath))
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < thumbFailTTLDuration()
}

func markThumbFailed(kind, rawPath, msg string) {
	// 记录失败路径与原因，便于按目录统计失败与重试、前端告警
	data, _ := json.Marshal(map[string]string{
		"path": rawPath,
		"at":   time.Now().Format(time.RFC3339),
		"msg":  msg,
	})
	_ = writeFileAtomic(thumbFailPath(kind, rawPath), data, 0o666)
}

// thumbFailItem 失败缩略图信息
type thumbFailItem struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Dir  string `json:"dir"`
	At   string `json:"at"`
	Msg  string `json:"msg"`
}

// listThumbFails 扫描 fail 标记文件，解析出失败路径（旧格式无内容视为路径未知）
func listThumbFails() []thumbFailItem {
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var items []thumbFailItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".fail") {
			continue
		}
		kind := strings.SplitN(e.Name(), "-", 2)[0]
		item := thumbFailItem{Kind: kind}
		if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && len(data) > 0 {
			var m map[string]string
			if json.Unmarshal(data, &m) == nil {
				item.Path = m["path"]
				item.At = m["at"]
				item.Msg = m["msg"]
				if item.Path != "" {
					item.Dir = stdpath.Dir(item.Path)
				}
			}
		}
		items = append(items, item)
	}
	return items
}

func ffmpegBin() (string, error) {
	ffmpegOnce.Do(func() {
		ffmpegPath, ffmpegErr = exec.LookPath("ffmpeg")
		if ffmpegErr != nil {
			log.Errorf("ffmpeg not found, video/audio thumbnails disabled: %s", ffmpegErr)
		}
	})
	return ffmpegPath, ffmpegErr
}

func thumbLinkHeader() http.Header {
	return http.Header{
		"User-Agent": []string{thumbUA},
	}
}

// thumbProxyForPath 返回缩略图读取使用的静态出站代理。
// 动态节点选择已移除；驱动与缩略图共用存储配置或全局 proxy_address。
func thumbProxyForPath(rawPath string) string {
	if rawPath != "" {
		storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
		if err == nil {
			if p, ok := storage.GetAddition().(interface{ GetProxy() string }); ok {
				if px := p.GetProxy(); px != "" {
					return px
				}
			}
		}
	}
	return conf.Conf.ProxyAddress
}

type thumbHTTPClientEntry struct {
	client *http.Client
	err    error
}

var thumbHTTPClients sync.Map

// thumbHTTPClient 为每个静态代理复用 Transport/Client，保留连接池。
func thumbHTTPClient(proxy string) (*http.Client, error) {
	if cached, ok := thumbHTTPClients.Load(proxy); ok {
		e := cached.(thumbHTTPClientEntry)
		return e.client, e.err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	insecureSkipVerify := conf.Conf != nil && conf.Conf.TlsInsecureSkipVerify
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecureSkipVerify}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Scheme == "" || u.Host == "" {
			if err == nil {
				err = fmt.Errorf("proxy URL must include scheme and host")
			}
			e := thumbHTTPClientEntry{err: fmt.Errorf("invalid proxy address %q: %w", proxy, err)}
			actual, _ := thumbHTTPClients.LoadOrStore(proxy, e)
			stored := actual.(thumbHTTPClientEntry)
			return stored.client, stored.err
		}
		transport.Proxy = http.ProxyURL(u)
	}
	e := thumbHTTPClientEntry{client: &http.Client{Transport: transport}}
	actual, _ := thumbHTTPClients.LoadOrStore(proxy, e)
	stored := actual.(thumbHTTPClientEntry)
	return stored.client, stored.err
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

func validateThumbRangeResponse(resp *http.Response, offset, limit int64) error {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		value := strings.TrimSpace(resp.Header.Get("Content-Range"))
		unit, rest, ok := strings.Cut(value, " ")
		if !ok || !strings.EqualFold(unit, "bytes") {
			return fmt.Errorf("invalid Content-Range %q", value)
		}
		bounds, _, ok := strings.Cut(rest, "/")
		if !ok {
			return fmt.Errorf("invalid Content-Range %q", value)
		}
		startText, endText, ok := strings.Cut(bounds, "-")
		if !ok {
			return fmt.Errorf("invalid Content-Range %q", value)
		}
		start, startErr := strconv.ParseInt(startText, 10, 64)
		end, endErr := strconv.ParseInt(endText, 10, 64)
		if startErr != nil || endErr != nil || start != offset || end != offset+limit-1 {
			return fmt.Errorf("unexpected Content-Range %q for bytes=%d-%d", value, offset, offset+limit-1)
		}
		return nil
	case http.StatusOK:
		// Some origins ignore Range for an initial read. It is safe only at offset 0;
		// callers still wrap the body in LimitReader.
		if offset == 0 {
			return nil
		}
		return fmt.Errorf("origin ignored Range request at offset %d", offset)
	default:
		return fmt.Errorf("download failed: %d %s", resp.StatusCode, resp.Status)
	}
}

func openThumbRange(ctx context.Context, link *model.Link, offset, limit int64, proxy string, timeout time.Duration) (io.ReadCloser, error) {
	if link == nil {
		return nil, errors.New("nil download link")
	}
	if offset < 0 || limit <= 0 || offset+limit-1 < offset {
		return nil, fmt.Errorf("invalid byte range offset=%d limit=%d", offset, limit)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	if link.RangeReader != nil {
		rc, err := link.RangeReader.RangeRead(requestCtx, http_range.Range{Start: offset, Length: limit})
		if err != nil {
			cancel()
			return nil, err
		}
		return &cancelReadCloser{ReadCloser: rc, cancel: cancel}, nil
	}
	if link.URL == "" {
		cancel()
		return nil, errors.New("download link has no URL or RangeReader")
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, link.URL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header = link.Header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1))
	client, err := thumbHTTPClient(proxy)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := validateThumbRangeResponse(resp, offset, limit); err != nil {
		_ = resp.Body.Close()
		cancel()
		return nil, err
	}
	return &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}, nil
}

// downloadRange 从链接读取 [offset, offset+limit) 字节到本地：
// 优先使用驱动提供的 RangeReader（本地/流式驱动），否则用 Range 请求下载（proxy 非空时走代理）。
func downloadRange(ctx context.Context, link *model.Link, dstPath string, offset, limit int64, proxy string) (int64, error) {
	rc, err := openThumbRange(ctx, link, offset, limit, proxy, 90*time.Second)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	f, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(rc, limit))
	if err != nil {
		return n, err
	}
	if n != limit {
		return n, fmt.Errorf("short range read: got %d bytes, want %d: %w", n, limit, io.ErrUnexpectedEOF)
	}
	return n, nil
}

// extractVideoFrame 从本地视频文件抽帧（-ss 3 失败回退 0s）
func extractVideoFrame(ctx context.Context, localPath string) ([]byte, error) {
	extract := func(ss string) ([]byte, error) {
		srcBuf := bytes.NewBuffer(nil)
		kwargs := ffmpeg.KwArgs{"noaccurate_seek": ""}
		if ss != "" {
			kwargs["ss"] = ss
		}
		stream := ffmpeg.Input(localPath, kwargs).
			Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg", "strict": "unofficial"}).
			GlobalArgs("-loglevel", "error").Silent(true).
			WithOutput(srcBuf, os.Stdout)
		setStreamContext(stream, ctx)
		if err := stream.Run(); err != nil {
			return nil, err
		}
		if srcBuf.Len() == 0 {
			return nil, fmt.Errorf("empty output")
		}
		return srcBuf.Bytes(), nil
	}
	var last []byte
	var lastErr error
	// 依次尝试多个取帧点：跳过空白帧（片头黑屏/纯色转场等），
	// 避免抽到单帧空白被误判"生成结果为空白图"。
	for _, ss := range []string{"3", "", "10", "30", "60"} {
		data, err := extract(ss)
		if err != nil {
			lastErr = err
			continue
		}
		thumb, err := encodeThumb(data)
		if err != nil {
			lastErr = err
			continue
		}
		last = thumb
		if !isBlankThumb(thumb) {
			return thumb, nil
		}
	}
	if last != nil {
		return last, nil
	}
	return nil, lastErr
}

// setStreamContext 让 ffmpeg-go stream 使用可取消的 context（保留 Stdout/Stderr 值），
// 队列暂停/清空时可杀进程
func setStreamContext(stream *ffmpeg.Stream, ctx context.Context) {
	if v, ok := stream.Context.Value("Stdout").(io.Writer); ok {
		ctx = context.WithValue(ctx, "Stdout", v)
	}
	if v, ok := stream.Context.Value("Stderr").(io.Writer); ok {
		ctx = context.WithValue(ctx, "Stderr", v)
	}
	stream.Context = ctx
}

const thumbFFmpegStderrLimit = 8 * 1024

// thumbLimitedBuffer 防止 ffmpeg 异常输出大量 stderr；其中内容只用于本地风控分类。
type thumbLimitedBuffer struct {
	buf bytes.Buffer
}

func (b *thumbLimitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < thumbFFmpegStderrLimit {
		n := thumbFFmpegStderrLimit - b.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		_, _ = b.buf.Write(p[:n])
	}
	return len(p), nil
}

func (b *thumbLimitedBuffer) String() string {
	return b.buf.String()
}

func isThumbRemoteRiskText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"403", "405", "forbidden", "blocked", "pmt"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isThumbRemoteRiskError(err error) bool {
	return err != nil && (errors.Is(err, errThumbRemoteRisk) || isThumbRemoteRiskText(err.Error()))
}

func ffmpegHTTPHeaders(header http.Header) string {
	if len(header) == 0 {
		return ""
	}
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		for _, value := range header.Values(key) {
			value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", "")
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

// extractVideoFrameAt 通过 ffmpeg HTTP Range 从远程 URL 指定时间点抽一帧（原始 mjpeg 字节）。
func extractVideoFrameAt(ctx context.Context, sourceURL string, header http.Header, proxy, ss string) ([]byte, error) {
	if sourceURL == "" {
		return nil, errors.New("remote media link has no URL")
	}
	srcBuf := bytes.NewBuffer(nil)
	var stderr thumbLimitedBuffer
	kwargs := ffmpeg.KwArgs{"noaccurate_seek": "", "rw_timeout": "20000000"}
	if ss != "" {
		kwargs["ss"] = ss
	}
	if headers := ffmpegHTTPHeaders(header); headers != "" {
		kwargs["headers"] = headers
	}
	if proxy != "" {
		kwargs["http_proxy"] = proxy
	}
	stream := ffmpeg.Input(sourceURL, kwargs).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg", "strict": "unofficial"}).
		GlobalArgs("-loglevel", "error").Silent(true).
		WithOutput(srcBuf, &stderr)
	setStreamContext(stream, ctx)
	if err := stream.Run(); err != nil {
		if isThumbRemoteRiskText(stderr.String()) || isThumbRemoteRiskText(err.Error()) {
			// 不把远程 URL、签名或请求头带回上层，只返回固定风控错误。
			return nil, errThumbRemoteRisk
		}
		return nil, err
	}
	if srcBuf.Len() == 0 {
		return nil, fmt.Errorf("empty output")
	}
	return srcBuf.Bytes(), nil
}

// extractVideoFrameRemote 通过 ffmpeg HTTP Range 直接远程抽帧（3s 处单帧缩略图），
// 适用于 moov 在文件尾部、本地切片无法解析的场景（只传输所需字节）。
// 深偏移 seek 对部分文件（moov 在尾部/结构稀疏）抽不到帧，回退到首帧（offset 0）。
func extractVideoFrameRemote(ctx context.Context, sourceURL string, header http.Header, proxy string) ([]byte, error) {
	var (
		last    []byte
		lastErr error
	)
	// 依次尝试多个远程取帧点，跳过空白帧（见 extractVideoFrame 注释）
	for _, ss := range []string{"3", "0", "10", "30", "60"} {
		data, err := extractVideoFrameAt(ctx, sourceURL, header, proxy, ss)
		if err != nil {
			if isThumbRemoteRiskError(err) {
				return nil, err
			}
			lastErr = err
			continue
		}
		thumb, err := encodeThumb(data)
		if err != nil {
			lastErr = err
			continue
		}
		last = thumb
		if !isBlankThumb(thumb) {
			return thumb, nil
		}
	}
	if last != nil {
		return last, nil
	}
	return nil, lastErr
}

// generateVideoAdaptiveFrame 长视频单帧缩略图（1x1）：按"中间优先"依次尝试远程 seek
// 深度 50%→30%→15%→7%→3%，最后兜底首帧（0），取第一个成功的帧（越深越接近中间内容）。
// 115 对大文件深偏移 Range 常返回 403（每次失败 ~0.5s 快速跳过），
// 部分文件深偏移 seek 抽不到帧（moov 在尾部），此时首帧兜底。
// 单帧+自适应比多帧网格更快更可靠。
func generateVideoAdaptiveFrame(ctx context.Context, sourceURL string, header http.Header, proxy string, duration float64) ([]byte, error) {
	if duration <= 0 {
		return nil, errors.New("invalid duration")
	}
	for _, ratio := range []float64{0.5, 0.3, 0.15, 0.07, 0.03, 0.0} {
		data, err := extractVideoFrameAt(ctx, sourceURL, header, proxy, fmt.Sprintf("%.2f", duration*ratio))
		if err != nil {
			if isThumbRemoteRiskError(err) {
				return nil, err
			}
			continue
		}
		thumb, err := encodeThumb(data)
		if err != nil {
			continue
		}
		// 跳过空白帧（该深度恰好为黑屏/纯色转场时继续试下一个深度）
		if isBlankThumb(thumb) {
			continue
		}
		return thumb, nil
	}
	return nil, errors.New("no frame extractable at any depth")
}

// extractAudioCover 从本地音频文件提取内嵌封面（无封面时返回 errThumbNoCover）
func extractAudioCover(ctx context.Context, localPath string) ([]byte, error) {
	srcBuf := bytes.NewBuffer(nil)
	stream := ffmpeg.Input(localPath).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg", "strict": "unofficial"}).
		GlobalArgs("-map", "0:v:0", "-an", "-loglevel", "error").Silent(true).
		WithOutput(srcBuf, os.Stdout)
	setStreamContext(stream, ctx)
	if err := stream.Run(); err != nil {
		return nil, errThumbNoCover
	}
	if srcBuf.Len() == 0 {
		return nil, errThumbNoCover
	}
	return encodeThumb(srcBuf.Bytes())
}

// resizeImageFile 将图片文件缩放为 PNG 缩略图
func resizeImageFile(localPath string) ([]byte, error) {
	img, err := imaging.Open(localPath, imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	return encodeThumbImage(img)
}

// resizeImageData 将图片字节缩放为 PNG 缩略图
func resizeImageData(data []byte) ([]byte, error) {
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	return encodeThumbImage(img)
}

func encodeThumbImage(img image.Image) ([]byte, error) {
	width := setting.GetInt(conf.ThumbWidth, thumbWidth)
	if width < 64 {
		width = 64
	}
	if width > 4096 {
		width = 4096
	}
	thumbImg := imaging.Resize(img, width, 0, imaging.Lanczos)
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, thumbImg, imaging.PNG); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeThumb(mjpeg []byte) ([]byte, error) {
	return resizeImageData(mjpeg)
}

// 缩略图路径索引：生成成功时记录路径，供管理页按目录统计已有缩略图
var (
	thumbIndexMu sync.Mutex
)

const thumbIndexFile = "index.jsonl"

func thumbIndexPath() string {
	return filepath.Join(thumbDir(), thumbIndexFile)
}

// thumbRecord 记录一条缩略图成功记录（append，含路径）
func thumbRecord(rawPath string) {
	if rawPath == "" {
		return
	}
	line := fmt.Sprintf(`{"path":%s,"at":%q}%s`,
		strconv.Quote(rawPath), time.Now().Format(time.RFC3339), "\n")
	thumbIndexMu.Lock()
	f, err := os.OpenFile(thumbIndexPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		thumbIndexMu.Unlock()
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
	// 索引超过 50000 行时截断重写（仅保留缓存文件仍存在的条目）
	needRewrite := false
	if fi, err := os.Stat(thumbIndexPath()); err == nil {
		needRewrite = fi.Size() > 4*1024*1024
	}
	thumbIndexMu.Unlock()
	if needRewrite {
		thumbRewriteIndex()
	}
	// 新缩略图生成成功：使顶部聚合统计失效，下次轮询立即重算（磁盘扫描廉价）
	thumbAggMu.Lock()
	thumbAggAt = time.Time{}
	thumbAggMu.Unlock()
}

// thumbRewriteIndex 重写索引：只保留仍有效（本地缓存存在或已上传到网盘）的条目
func thumbRewriteIndex() {
	thumbIndexMu.Lock()
	defer thumbIndexMu.Unlock()
	lines := readThumbIndex()
	cloud := readThumbCloudIndex()
	var b strings.Builder
	for _, p := range lines {
		h := thumbHash(p)
		if _, err := os.Stat(filepath.Join(thumbDir(), "video-"+h+".png")); err == nil {
			b.WriteString(fmt.Sprintf(`{"path":%s,"at":""}%s`, strconv.Quote(p), "\n"))
		} else if cloud[p] {
			// 本地已删除（上传到网盘后），网盘仍持有缩略图：保留索引
			b.WriteString(fmt.Sprintf(`{"path":%s,"at":""}%s`, strconv.Quote(p), "\n"))
		}
	}
	_ = writeFileAtomic(thumbIndexPath(), []byte(b.String()), 0o666)
}

// readThumbIndex 读取索引中的路径列表（去重，保留先后顺序）
func readThumbIndex() []string {
	data, err := os.ReadFile(thumbIndexPath())
	if err != nil {
		return nil
	}
	var paths []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Path != "" {
			if _, ok := seen[m.Path]; ok {
				continue
			}
			seen[m.Path] = struct{}{}
			paths = append(paths, m.Path)
		}
	}
	return paths
}

// ---------- 网盘缩略图索引（cloud.jsonl）----------
// 记录已成功上传到网盘 _thumbnails 的路径；用于统计"网盘+本地"并集与索引重写时保留网盘条目。

var (
	thumbCloudMu  sync.Mutex
	thumbCloudSet map[string]bool
)

const thumbCloudFile = "cloud.jsonl"

func thumbCloudIndexPath() string {
	return filepath.Join(thumbDir(), thumbCloudFile)
}

// thumbCloudRecord 记录一条已上传到网盘的缩略图（内存去重 + append 持久化）
func thumbCloudRecord(rawPath string) {
	if rawPath == "" {
		return
	}
	thumbCloudMu.Lock()
	if thumbCloudSet == nil {
		thumbCloudSet = readThumbCloudIndex()
	}
	if thumbCloudSet[rawPath] {
		thumbCloudMu.Unlock()
		return
	}
	thumbCloudSet[rawPath] = true
	line := fmt.Sprintf(`{"path":%s,"at":%q}%s`,
		strconv.Quote(rawPath), time.Now().Format(time.RFC3339), "\n")
	f, err := os.OpenFile(thumbCloudIndexPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		thumbCloudMu.Unlock()
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
	thumbCloudMu.Unlock()

	// 网盘上传成功：增量更新网盘计数，顶部统计立即可见（无需等 10 分钟网络重算）。
	// 仅当网盘计数已计算过才递增，避免从未计算时从 0 起跳导致与真实值漂移；
	// 未计算时保持无效，下一次 ThumbStatus 会重新全量计算。
	thumbCloudStatsMu.Lock()
	if !thumbCloudStatsAt.IsZero() {
		thumbCloudStatsVal.cloud++
		if _, err := os.Stat(thumbCachePath(thumbKindVideo, rawPath)); err == nil {
			thumbCloudStatsVal.overlap++
		}
		thumbCloudStatsAt = time.Now()
	}
	thumbCloudStatsMu.Unlock()
	thumbAggMu.Lock()
	thumbAggAt = time.Time{}
	thumbAggMu.Unlock()
}

// readThumbCloudIndex 读取网盘已上传缩略图的路径集合
func readThumbCloudIndex() map[string]bool {
	data, err := os.ReadFile(thumbCloudIndexPath())
	if err != nil {
		return map[string]bool{}
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Path != "" {
			out[m.Path] = true
		}
	}
	return out
}

// thumbCloudCount 网盘已上传缩略图数量（内存缓存）
func thumbCloudCount() int {
	thumbCloudMu.Lock()
	defer thumbCloudMu.Unlock()
	if thumbCloudSet == nil {
		thumbCloudSet = readThumbCloudIndex()
	}
	return len(thumbCloudSet)
}

// thumbCloudStats 统计网盘 _thumbnails 实际缩略图总数，以及"本地存在且网盘清单也有"的重叠数。
// 以实际目录清单为准（loadRemoteThumbListing 带缓存），而非仅 cloud.jsonl 上传记录，
// 覆盖此前上传/手动上传而未写入 cloud.jsonl 的文件。聚合结果缓存，避免高频轮询反复列目录。
func thumbCloudStats(ctx context.Context) (cloudCount, overlap int) {
	thumbCloudStatsMu.Lock()
	if time.Since(thumbCloudStatsAt) < thumbCloudStatsTTL {
		v := thumbCloudStatsVal
		thumbCloudStatsMu.Unlock()
		return v.cloud, v.overlap
	}
	thumbCloudStatsMu.Unlock()
	cloud, ov := computeThumbCloudStats(ctx)
	thumbCloudStatsMu.Lock()
	thumbCloudStatsVal.cloud, thumbCloudStatsVal.overlap = cloud, ov
	thumbCloudStatsAt = time.Now()
	thumbCloudStatsMu.Unlock()
	return cloud, ov
}

func computeThumbCloudStats(ctx context.Context) (cloudCount, overlap int) {
	indexed := readThumbIndex()
	cache := map[string]map[string]bool{}
	for _, p := range indexed {
		dir := stdpath.Dir(p)
		if dir == "" || dir == "." {
			continue
		}
		names, ok := cache[dir]
		if !ok {
			names = loadRemoteThumbListing(ctx, dir, folderNameOnly{thumbFolderNameForPath(dir)})
			cache[dir] = names
			cloudCount += len(names)
		}
		if _, err := os.Stat(thumbCachePath(thumbKindVideo, p)); err == nil {
			if names[remoteThumbName(p)] {
				overlap++
			}
		}
	}
	return cloudCount, overlap
}

var (
	thumbCloudStatsMu  sync.Mutex
	thumbCloudStatsAt  time.Time
	thumbCloudStatsVal struct{ cloud, overlap int }
)

const thumbCloudStatsTTL = 10 * time.Minute

// serveThumb 通用缩略图入口：缓存命中直接返回，未命中则串行生成
func serveThumb(c *gin.Context, kind, rawPath string, generate func() ([]byte, error)) {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	cachePath := thumbCachePath(kind, rawPath)
	if data, err := os.ReadFile(cachePath); err == nil {
		serveThumbPNG(c, data)
		return
	}
	if thumbFailed(kind, rawPath) {
		serveThumbPlaceholder(c)
		return
	}
	// 用户手动排除的视频不生成缩略图（避免无谓下载）
	if readThumbExcluded()[rawPath] {
		serveThumbPlaceholder(c)
		return
	}
	// 115 风控中禁止下载生成（视频缩略图需从网盘下载片段，会加剧风控）
	if blocked, _ := isStorageBlocked(rawPath); blocked {
		serveThumbPlaceholder(c)
		return
	}

	if !thumbAcquire(c.Request.Context(), 0) {
		// 并发占用：返回占位图（不写负缓存，下次有机会自动重试）
		serveThumbPlaceholder(c)
		return
	}
	defer thumbRelease()

	if data, err := os.ReadFile(cachePath); err == nil {
		serveThumbPNG(c, data)
		return
	}

	// 本地未命中：若网盘 _thumbnails 已有上传副本（如"上传→清空本地"场景），先恢复避免重复生成
	if kind == thumbKindVideo {
		if data, ok := tryRestoreRemoteThumb(c.Request.Context(), rawPath); ok {
			_ = writeFileAtomic(cachePath, data, 0o666)
			thumbRecord(rawPath)
			serveThumbPNG(c, data)
			return
		}
	}

	png, err := generateThumbOnce(kind, rawPath, generate)
	if err != nil {
		if errors.Is(err, errThumbNoCover) {
			markThumbFailed(kind, rawPath, "无封面/无法抽帧")
			serveThumbPlaceholder(c)
			return
		}
		if errors.Is(err, errThumbTooLarge) {
			markThumbFailed(kind, rawPath, "文件过大")
			serveThumbPlaceholder(c)
			return
		}
		// 所有生成失败都写负缓存，避免反复重试加重风控
		markThumbFailed(kind, rawPath, thumbFailReason(err))
		log.Warnf("thumb generate failed [%s] %s: %v", kind, rawPath, err)
		serveThumbPlaceholder(c)
		return
	}
	if isBlankThumb(png) {
		markThumbFailed(kind, rawPath, "生成结果为空白图")
		log.Warnf("thumb blank [%s] %s", kind, rawPath)
		serveThumbPlaceholder(c)
		return
	}
	_ = writeFileAtomic(cachePath, png, 0o666)
	thumbRecord(rawPath)
	serveThumbPNG(c, png)
}

// serveThumbPlaceholder 返回 1x1 透明 PNG 占位图（避免前端 img 破图）
func serveThumbPlaceholder(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=60")
	c.Data(200, "image/png", thumbPlaceholderPNG)
}

// thumbPlaceholderPNG 1x1 透明 PNG
var thumbPlaceholderPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

// downloadRangeBytes 从链接读取 [offset, offset+limit) 字节返回。
func downloadRangeBytes(ctx context.Context, link *model.Link, offset, limit int64, proxy string) ([]byte, error) {
	rc, err := openThumbRange(ctx, link, offset, limit, proxy, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != limit {
		return nil, fmt.Errorf("short range read: got %d bytes, want %d: %w", len(data), limit, io.ErrUnexpectedEOF)
	}
	return data, nil
}

// moov 探测结果缓存（moov 位置不变，24h 内复用，省一次 Range 请求）
var (
	moovCacheMu sync.Mutex
	moovCache   = map[string]moovCacheEntry{}
)

type moovCacheEntry struct {
	atTail bool
	at     time.Time
}

const moovCacheTTL = 24 * time.Hour

// moovAtTail 探测 moov 元数据是否位于文件尾部（下载末尾 64KB 查找 moov 标记）
func moovAtTail(ctx context.Context, link *model.Link, size int64, rawPath string) (bool, error) {
	moovCacheMu.Lock()
	if e, ok := moovCache[rawPath]; ok && time.Since(e.at) < moovCacheTTL {
		moovCacheMu.Unlock()
		return e.atTail, nil
	}
	moovCacheMu.Unlock()
	tailLen := int64(64 * 1024)
	if size < tailLen {
		return false, nil
	}
	proxy := thumbProxyForPath(rawPath)
	data, err := downloadRangeBytes(ctx, link, size-tailLen, tailLen, proxy)
	if err != nil {
		if isThumbRemoteRiskError(err) {
			return false, errThumbRemoteRisk
		}
		return false, nil
	}
	atTail := bytes.Contains(data, []byte("moov"))
	moovCacheMu.Lock()
	moovCache[rawPath] = moovCacheEntry{atTail: atTail, at: time.Now()}
	if len(moovCache) > 20000 {
		for k, v := range moovCache {
			if time.Since(v.at) > moovCacheTTL {
				delete(moovCache, k)
			}
		}
	}
	moovCacheMu.Unlock()
	return atTail, nil
}

// generateVideoThumb 生成视频缩略图（直接请求与预热共用）。
// 若经代理下载/抽帧得到空白图（代理中继损坏视频字节的典型表现），
// 自动回退直连重新生成一次。
func generateVideoThumb(ctx context.Context, rawPath string) ([]byte, error) {
	thumbGenerationAdmission.RLock()
	defer thumbGenerationAdmission.RUnlock()
	png, err := generateVideoThumbInner(ctx, rawPath, true)
	if err == nil && isBlankThumb(png) {
		log.Warnf("thumb blank via configured route, retry direct: %s", rawPath)
		if png2, err2 := generateVideoThumbInner(ctx, rawPath, false); err2 == nil {
			return png2, nil
		}
	}
	return png, err
}

// generateVideoThumbInner 生成视频缩略图。useProxy=false 时跳过代理直连下载。
func generateVideoThumbInner(ctx context.Context, rawPath string, useProxy bool) ([]byte, error) {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	if _, err := ffmpegBin(); err != nil {
		return nil, err
	}
	maxSize := int64(setting.GetInt(conf.ThumbVideoMaxSize, 0))
	link, obj, err := fs.Link(ctx, rawPath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	proxy := thumbProxyForPath(rawPath)
	if !useProxy {
		proxy = ""
	}
	size := obj.GetSize()
	// 0 = 不限大小（缩略图只下载开头/末尾 3MB 片段或远程抽帧，不下载整文件，大文件也安全）
	if maxSize > 0 && size > maxSize {
		return nil, errThumbTooLarge
	}
	remoteURL := link.URL

	// moov 在文件尾部时本地片段必然无法解析；长视频探测时长后取"中间单帧"（1x1），
	// 短视频直接远程抽 3s 单帧
	atTail, err := moovAtTail(ctx, link, size, rawPath)
	if err != nil {
		return nil, err
	}
	if atTail {
		if size > thumbProbeMinSize {
			if dur := probeVideoDuration(ctx, rawPath); dur > thumbMosaicLongSec {
				if data, err := generateVideoAdaptiveFrame(ctx, remoteURL, link.Header, proxy, dur); err == nil {
					return data, nil
				} else if isThumbRemoteRiskError(err) {
					return nil, err
				}
			}
		}
		return extractVideoFrameRemote(ctx, remoteURL, link.Header, proxy)
	}

	// 长视频：探测时长后取"中间单帧"（1x1，中间内容）；失败降级本地头部抽帧
	if size > thumbProbeMinSize {
		if dur := probeVideoDuration(ctx, rawPath); dur > thumbMosaicLongSec {
			if data, err := generateVideoAdaptiveFrame(ctx, remoteURL, link.Header, proxy, dur); err == nil {
				return data, nil
			} else if isThumbRemoteRiskError(err) {
				return nil, err
			}
			log.Debugf("middle frame failed for %s, fallback to head chunk", rawPath)
		}
	}

	tmpFile, err := newThumbTempPath(thumbKindVideo, rawPath, ".tmp.mp4")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile)
	if size <= thumbChunkSize {
		if _, err := downloadRange(ctx, link, tmpFile, 0, size, proxy); err != nil {
			return nil, err
		}
		return extractVideoFrame(ctx, tmpFile)
	}
	// 下载开头片段（moov 在头部时常见情况；片段不够（大 moov）自动加大重试）
	if data, ok, err := thumbExtractRange(ctx, link, tmpFile, 0, size, proxy); err != nil {
		return nil, err
	} else if ok {
		return data, nil
	}
	// moov 位于文件尾部（探测失败或非标准容器）：ffmpeg 直接读取驱动返回的 URL/Header，
	// 不再绕公共 /d，也不依赖外部请求 Host。
	if data, err := extractVideoFrameRemote(ctx, remoteURL, link.Header, proxy); err == nil {
		return data, nil
	} else if isThumbRemoteRiskError(err) {
		return nil, err
	}
	// 最后兜底：下载末尾片段（moov 在尾部且本地可解析时有效；同样自动加大片段）
	if data, ok, err := thumbExtractRange(ctx, link, tmpFile, size-thumbChunkSize, size, proxy); err != nil {
		return nil, err
	} else if ok {
		return data, nil
	}
	return nil, errors.New("unable to extract any video frame")
}

// thumbExtractRange 下载 [start, start+chunk) 片段并抽帧；抽帧失败自动加大片段。
// 大 moov 的视频（样本表很大）需要更大的头部/尾部片段才能解析；
// 大文件直接从更大片段开始，避免小片段反复失败。
func thumbExtractRange(ctx context.Context, link *model.Link, tmpFile string, start, size int64, proxy string) ([]byte, bool, error) {
	var sizes []int64
	if size > 512*1024*1024 {
		sizes = []int64{16 * 1024 * 1024, 32 * 1024 * 1024, 64 * 1024 * 1024}
	} else {
		sizes = []int64{thumbChunkSize, 8 * 1024 * 1024, 16 * 1024 * 1024, 32 * 1024 * 1024}
	}
	for _, chunk := range sizes {
		limit := chunk
		if start+limit > size {
			limit = size - start
		}
		if limit <= 0 {
			break
		}
		if _, err := downloadRange(ctx, link, tmpFile, start, limit, proxy); err != nil {
			if isThumbRemoteRiskError(err) {
				return nil, false, err
			}
			continue
		}
		if data, err := extractVideoFrame(ctx, tmpFile); err == nil {
			return data, true, nil
		}
	}
	return nil, false, nil
}

// thumbGenPower 返回生成强度参数。worker 与准入上限统一使用 thumb_concurrency，
// 防止后台 worker、浏览器请求和候选任务绕过用户配置。
type thumbGenPowerCfg struct {
	Workers       int
	BatchInterval time.Duration
	AcquireLimit  int
	FrameInterval time.Duration
	TaskInterval  time.Duration // 批内每个任务处理完后的间隔（降低请求/下载频率）
	EnqueueMax    int
}

func thumbGenPower() thumbGenPowerCfg {
	limit := setting.GetInt(conf.ThumbConcurrency, 8)
	if limit < 1 {
		limit = 1
	}
	if limit > 64 {
		limit = 64
	}
	return thumbGenPowerCfg{Workers: limit, AcquireLimit: limit, EnqueueMax: 2048}
}

// prewarmStart 启动预热 worker（多 worker 并发生成，不做节流）
func prewarmStart() {
	prewarmOnce.Do(func() {
		prewarmCh = make(chan thumbPrewarmTask, 2048)
		n := thumbGenPower().Workers
		for i := 0; i < n; i++ {
			go prewarmWorker()
		}
	})
}

// ---------- 自动上传 worker ----------

var autoUploadOnce sync.Once

// autoUploadStart 启动自动上传循环（幂等）；开启后定期扫描本地未上传缩略图并入队。
// 扫描只用本地缓存索引（index.jsonl + cloud.jsonl），不发 115 请求，防风控。
func autoUploadStart() {
	autoUploadOnce.Do(func() {
		go autoUploadWorker()
	})
}

const autoUploadInterval = 60 * time.Second

func autoUploadWorker() {
	for {
		time.Sleep(autoUploadInterval)
		autoUploadScanOnce()
	}
}

// autoUploadScanOnce 执行一轮自动上传扫描：本地已有缩略图但网盘（cloud 索引）没有的，
// 且当前不在上传队列/未处理/未失败的，加入上传队列。
func autoUploadScanOnce() {
	if setting.GetStr(conf.ThumbAutoUpload, "false") != "true" {
		return
	}
	if thumbUploadPaused.Load() {
		return
	}
	cloudSet := readThumbCloudIndex()
	var toUpload []string
	for _, p := range readThumbIndex() {
		if cloudSet[p] {
			continue
		}
		if _, err := os.Stat(thumbCachePath(thumbKindVideo, p)); err != nil {
			continue
		}
		toUpload = append(toUpload, p)
	}
	if len(toUpload) > 0 {
		log.Infof("thumb auto upload: %d local thumbnails not uploaded, enqueue", len(toUpload))
		thumbUploadEnqueue(toUpload)
	}
}

// StartThumbAuto 启动自动上传循环（服务启动时调用；也随开关启用时启动）
func StartThumbAuto() {
	autoUploadStart()
}

// ---------- 预热 worker ----------

// prewarmWorker 取到一个任务立即处理，不做批内/批间节流
func prewarmWorker() {
	for {
		// 暂停时阻塞等待恢复
		for thumbQueuePaused.Load() {
			time.Sleep(500 * time.Millisecond)
		}
		task, ok := <-prewarmCh
		if !ok {
			return
		}
		// pause 可能发生在 worker 已经阻塞于 channel receive 之后；取到任务后再检查一次，
		// 保证“暂停”不会额外漏跑一个任务。刚取出一个元素后 channel 必有回填空间。
		if thumbQueuePaused.Load() {
			prewarmRequeue(task)
			continue
		}
		processTask(task)
	}
}

// thumbActiveWorkers 当前正在处理缩略图生成的 worker 数（供前端进度条显示）
var thumbActiveWorkers int32

// thumbActiveTasks 进行中（worker 正在处理）的缩略图任务，供前端展示"正在生成 N 个/哪些文件"
var (
	thumbActiveMu     sync.Mutex
	thumbActiveTasks  = map[string]time.Time{}          // rawPath -> startedAt
	thumbActiveCancel = map[string]context.CancelFunc{} // rawPath -> cancel
	thumbGenEpoch     int64                             // 生成代际：暂停/清空时递增，用于丢弃旧代任务
)

// 候选九宫格生成是用户主动触发的远程取帧操作：
// 同一时间只允许一个候选任务，并与预热 worker 做准入隔离，避免叠加请求触发 115 风控。
var (
	thumbCandidateGate       = make(chan struct{}, 1)
	thumbCandidateActive     atomic.Bool
	thumbGenerationAdmission sync.RWMutex
)

func thumbActiveTrack(rawPath string, active bool) {
	thumbActiveMu.Lock()
	if active {
		thumbActiveTasks[rawPath] = time.Now()
	} else {
		delete(thumbActiveTasks, rawPath)
	}
	thumbActiveMu.Unlock()
}

func thumbActiveCancelAdd(rawPath string, cancel context.CancelFunc) {
	thumbActiveMu.Lock()
	thumbActiveCancel[rawPath] = cancel
	thumbActiveMu.Unlock()
}

func thumbActiveCancelDel(rawPath string) {
	thumbActiveMu.Lock()
	delete(thumbActiveCancel, rawPath)
	thumbActiveMu.Unlock()
}

func thumbActiveTasksSnapshot() []gin.H {
	thumbActiveMu.Lock()
	defer thumbActiveMu.Unlock()
	out := make([]gin.H, 0, len(thumbActiveTasks))
	for path, at := range thumbActiveTasks {
		out = append(out, gin.H{"path": path, "since": at.Unix()})
	}
	return out
}

// cancelActiveGeneration 取消所有进行中的生成任务（杀 ffmpeg 进程），并清空进行中列表。
// 同时递增代际，防止竞态下后登记的任务结果被误存。
func cancelActiveGeneration() {
	atomic.AddInt64(&thumbGenEpoch, 1)
	thumbActiveMu.Lock()
	for _, cancel := range thumbActiveCancel {
		cancel()
	}
	thumbActiveCancel = map[string]context.CancelFunc{}
	thumbActiveTasks = map[string]time.Time{}
	thumbActiveMu.Unlock()
}

// thumbTaskCancelled 判断任务是否被暂停/清空取消（用于丢弃结果、不重试）
func thumbTaskCancelled(genCtx context.Context, epoch int64) bool {
	if genCtx != nil && genCtx.Err() != nil {
		return true
	}
	return atomic.LoadInt64(&thumbGenEpoch) != epoch
}

func processTask(task thumbPrewarmTask) {
	// 风控中不循环重排队，等下一次浏览或手动操作再入队。
	if blocked, _ := isStorageBlocked(task.rawPath); blocked {
		prewarmDone.Delete(task.rawPath)
		return
	}
	cachePath := thumbCachePath(task.kind, task.rawPath)
	if _, err := os.ReadFile(cachePath); err == nil {
		prewarmDone.Store(task.rawPath, struct{}{})
		return
	}
	if thumbFailed(task.kind, task.rawPath) {
		prewarmDone.Store(task.rawPath, struct{}{})
		return
	}
	if thumbCandidateActive.Load() {
		// 候选任务正在进行，暂不让预热 worker 发起新的远程请求
		time.Sleep(250 * time.Millisecond)
		prewarmRequeue(task)
		return
	}
	if !thumbAcquire(context.Background(), 2*time.Second) {
		// 并发资源被直接请求或候选生成占用，让位稍后重试
		prewarmRequeue(task)
		return
	}
	atomic.AddInt32(&thumbActiveWorkers, 1)
	defer atomic.AddInt32(&thumbActiveWorkers, -1)
	defer thumbRelease()
	epoch := atomic.LoadInt64(&thumbGenEpoch)
	genCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	thumbActiveTrack(task.rawPath, true)
	thumbActiveCancelAdd(task.rawPath, cancel)
	defer func() {
		thumbActiveTrack(task.rawPath, false)
		thumbActiveCancelDel(task.rawPath)
	}()
	// 所有 Range 与 ffmpeg 操作共享同一个可取消 context；超时会真正终止子进程和读取。
	png, err := generateThumbOnce(task.kind, task.rawPath, func() ([]byte, error) {
		return generateVideoThumb(genCtx, task.rawPath)
	})
	if errors.Is(genCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("thumb generation timeout (90s): %w", context.DeadlineExceeded)
	}
	// 暂停/清空队列导致的取消：丢弃结果、不缓存、不重试（可重新生成）
	if thumbTaskCancelled(genCtx, epoch) {
		prewarmDone.Delete(task.rawPath)
		return
	}
	if err != nil {
		// 永久性失败（文件被 115 拦截/结构损坏/文件过大/无封面等）：写失败标记、不重试，让用户可见
		if isPermanentThumbError(err) || errors.Is(err, errThumbTooLarge) || errors.Is(err, errThumbNoCover) {
			log.Warnf("thumb prewarm permanent fail [%s] %s: %v", task.kind, task.rawPath, err)
			markThumbFailed(task.kind, task.rawPath, thumbFailReason(err))
			prewarmDone.Store(task.rawPath, struct{}{})
			return
		}
		// 临时失败不在 worker 内睡眠或循环重试，避免队列风暴；后续浏览可重新入队。
		log.Warnf("thumb prewarm transient fail [%s] %s: %v", task.kind, task.rawPath, err)
		prewarmDone.Delete(task.rawPath)
		return
	}
	// 空白/纯色缩略图：视为生成失败（写失败标记、不缓存），避免占着"已有缩略图"名额
	if isBlankThumb(png) {
		log.Warnf("thumb prewarm blank [%s] %s", task.kind, task.rawPath)
		markThumbFailed(task.kind, task.rawPath, "生成结果为空白图")
		prewarmDone.Store(task.rawPath, struct{}{})
		return
	}
	// remote 模式：缩略图上传到视频所在网盘目录，本机不落盘
	if addition := remoteThumbStore(task.rawPath); addition != nil {
		uploadCtx, uploadCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := uploadThumbRemote(uploadCtx, task.rawPath, addition, png); err != nil {
			log.Warnf("thumb prewarm upload remote failed %s: %v", task.rawPath, err)
		}
		uploadCancel()
		remoteThumbCacheSet(task.rawPath, png)
		_ = writeFileAtomic(cachePath, png, 0o666)
	} else {
		_ = writeFileAtomic(cachePath, png, 0o666)
	}
	thumbRecord(task.rawPath)
	// 生成成功后清除远程 miss 标记（remote 模式可能之前被标 miss）
	remoteThumbMissClear(task.rawPath)
	prewarmDone.Store(task.rawPath, struct{}{})
}

// 目录缩略图清单缓存：列表时一次性列出 _thumbnails 文件名（1 次 API），
// 之后 /vt 读取依据清单判断远程缩略图是否存在，避免每个视频都查询 115
var (
	thumbListingMu    sync.Mutex
	thumbListing      = map[string]thumbListingEntry{}
	thumbListingProbe sync.Map // 目录防抖
)

type thumbListingEntry struct {
	names map[string]bool
	at    time.Time
}

const (
	thumbListingTTL = 5 * time.Minute
	thumbListingDeb = 4 * time.Minute
)

// loadRemoteThumbListing 列出目录 _thumbnails 文件夹的文件名集合（1 次 API，带缓存）
func loadRemoteThumbListing(ctx context.Context, dirPath string, addition interface {
	ThumbFolderName() string
}) map[string]bool {
	if dirPath != "" && !strings.HasPrefix(dirPath, "/") {
		dirPath = "/" + dirPath
	}
	thumbListingMu.Lock()
	if e, ok := thumbListing[dirPath]; ok && time.Since(e.at) < thumbListingTTL {
		thumbListingMu.Unlock()
		return e.names
	}
	thumbListingMu.Unlock()
	if _, probing := thumbListingProbe.LoadOrStore(dirPath, struct{}{}); probing {
		return nil
	}
	defer thumbListingProbe.Delete(dirPath)

	thumbDir := dirPath + "/" + addition.ThumbFolderName()
	objs, err := fs.List(ctx, thumbDir, &fs.ListArgs{NoLog: true})
	names := map[string]bool{}
	if err == nil {
		for _, obj := range objs {
			names[obj.GetName()] = true
		}
	}
	thumbListingMu.Lock()
	thumbListing[dirPath] = thumbListingEntry{names: names, at: time.Now()}
	if len(thumbListing) > 2000 {
		for k, v := range thumbListing {
			if time.Since(v.at) > thumbListingTTL {
				delete(thumbListing, k)
			}
		}
	}
	thumbListingMu.Unlock()
	return names
}

// preloadRemoteListing 异步预载目录缩略图清单（列表时不阻塞）
func preloadRemoteListing(ctx context.Context, dirPath string, addition interface {
	ThumbFolderName() string
}) {
	if dirPath != "" && !strings.HasPrefix(dirPath, "/") {
		dirPath = "/" + dirPath
	}
	thumbListingMu.Lock()
	if e, ok := thumbListing[dirPath]; ok && time.Since(e.at) < thumbListingTTL {
		thumbListingMu.Unlock()
		return
	}
	thumbListingMu.Unlock()
	go func() {
		loadRemoteThumbListing(context.WithoutCancel(ctx), dirPath, addition)
	}()
}

// remoteThumbInListing 判断目录清单中是否存在该视频的缩略图
func remoteThumbInListing(dirPath, rawPath string) (bool, bool) {
	thumbListingMu.Lock()
	e, ok := thumbListing[dirPath]
	thumbListingMu.Unlock()
	if !ok || time.Since(e.at) >= thumbListingTTL {
		return false, false
	}
	_, exists := e.names[remoteThumbName(rawPath)]
	return exists, true
}

// thumbListingInvalidate 清除指定目录的远程缩略图清单缓存。
// 删除缩略图后调用，否则旧的清单缓存会让目录树计数/生成判断仍认为缩略图存在。
func thumbListingInvalidate(dirPath string) {
	if dirPath != "" && !strings.HasPrefix(dirPath, "/") {
		dirPath = "/" + dirPath
	}
	thumbListingMu.Lock()
	delete(thumbListing, dirPath)
	thumbListingMu.Unlock()
}

// thumbDeleteReset 删除缩略图后的通用清理：清除该路径的预热完成标记、
// 远程内存缓存与目录防抖，使其可被重新生成/上传。
func thumbDeleteReset(paths []string) {
	dirs := map[string]struct{}{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		prewarmDone.Delete(p)
		remoteThumbCacheMu.Lock()
		delete(remoteThumbCache, p)
		remoteThumbCacheMu.Unlock()
		if d := stdpath.Dir(p); d != "" && d != "." {
			dirs[d] = struct{}{}
		}
	}
	for d := range dirs {
		prewarmDirDeb.Delete(d)
	}
}

// thumbStatsInvalidate 使聚合统计与网盘计数缓存失效，下次状态/树请求重新计算
func thumbStatsInvalidate() {
	thumbCloudStatsMu.Lock()
	thumbCloudStatsAt = time.Time{}
	thumbCloudStatsMu.Unlock()
	thumbAggMu.Lock()
	thumbAggAt = time.Time{}
	thumbAggMu.Unlock()
}

// thumbCloudRemove 从网盘索引(cloud.jsonl)移除指定路径（删除缩略图后同步，
// 避免自动上传/计数仍认为已上传）
func thumbCloudRemove(paths []string) {
	if len(paths) == 0 {
		return
	}
	thumbCloudMu.Lock()
	if thumbCloudSet == nil {
		thumbCloudSet = readThumbCloudIndex()
	}
	removed := false
	for _, p := range paths {
		if p != "" && thumbCloudSet[p] {
			delete(thumbCloudSet, p)
			removed = true
		}
	}
	if removed {
		lines := make([]string, 0, len(thumbCloudSet))
		for p := range thumbCloudSet {
			lines = append(lines, fmt.Sprintf(`{"path":%s,"at":%q}`,
				strconv.Quote(p), time.Now().Format(time.RFC3339)))
		}
		sort.Strings(lines)
		_ = writeFileAtomic(thumbCloudIndexPath(), []byte(strings.Join(lines, "\n")+"\n"), 0o666)
	}
	thumbCloudMu.Unlock()
}

func prewarmRequeue(task thumbPrewarmTask) bool {
	select {
	case prewarmCh <- task:
		return true
	default:
		prewarmDone.Delete(task.rawPath)
		return false
	}
}

// prewarmEnqueue 入队预热任务（去重）。队列满时丢弃本次预热，等待后续浏览重新发现。
func prewarmEnqueue(kind, rawPath string) {
	prewarmStart()
	if _, done := prewarmDone.Load(rawPath); done {
		return
	}
	prewarmDone.Store(rawPath, struct{}{})
	select {
	case prewarmCh <- thumbPrewarmTask{kind: kind, rawPath: rawPath}:
	default:
		prewarmDone.Delete(rawPath)
	}
}

// prewarmDir 浏览目录时预热视频缩略图（带目录防抖）
func prewarmDir(c *gin.Context, parent string, objs []model.Obj) {
	if setting.GetStr(conf.ThumbPrewarm, "true") != "true" {
		return
	}
	if last, ok := prewarmDirDeb.Load(parent); ok {
		if time.Since(last.(time.Time)) < prewarmDebounce {
			return
		}
	}
	prewarmDirDeb.Store(parent, time.Now())
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		if utils.GetFileType(obj.GetName()) != conf.VIDEO {
			continue
		}
		prewarmEnqueue(thumbKindVideo, parent+"/"+obj.GetName())
	}
}

func serveThumbPNG(c *gin.Context, data []byte) {
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(200, "image/png", data)
}

// VideoThumb GET /vt/*path
// 视频文件缩略图：remote 模式生成后上传到视频所在网盘目录的 _thumbnails 文件夹，
// 之后从网盘读取（本机不落盘）；local 模式保持本地缓存
func VideoThumb(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	startThumbCleanup()
	if remoteStore := remoteThumbStore(rawPath); remoteStore != nil {
		serveRemoteVideoThumb(c, rawPath, remoteStore)
		return
	}
	serveThumb(c, thumbKindVideo, rawPath, func() ([]byte, error) {
		return generateVideoThumb(c.Request.Context(), rawPath)
	})
}

// remoteThumbAddition 判断视频所在存储是否为远程缩略图模式，返回 Addition
func remoteThumbStore(rawPath string) interface {
	ThumbStoreRemote() bool
	ThumbFolderName() string
} {
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		return nil
	}
	if r, ok := storage.GetAddition().(interface {
		ThumbStoreRemote() bool
		ThumbFolderName() string
	}); ok && r.ThumbStoreRemote() {
		return r
	}
	return nil
}

// remoteThumbCache 远程缩略图的内存缓存（仅 remote 模式，重启后可重新从网盘读取）
var (
	remoteThumbCacheMu   sync.Mutex
	remoteThumbCache     = map[string]remoteThumbEntry{}
	remoteThumbCacheSize = 0
)

type remoteThumbEntry struct {
	data []byte
	at   time.Time
}

const (
	remoteThumbCacheMax = 64 * 1024 * 1024 // 内存缓存上限 64MB
	remoteThumbCacheTTL = time.Hour
)

// sanitize115Name 清除 115 禁止/不安全的文件名字符，仅保留字母数字中文与常见符号
var sanitize115Re = regexp.MustCompile(`[^\p{L}\p{N}\-_\s\.\(\)\[\]]+`)

func sanitize115Name(name string) string {
	name = sanitize115Re.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	// 按 rune 截断，避免按字节切断 UTF-8 中文字符产生非法字节序列
	// （115 files/init 会以 990005 非法参数错误拒绝非法 UTF-8 文件名）
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40])
	}
	if name == "" {
		name = "thumb"
	}
	return name
}

func remoteThumbName(rawPath string) string {
	h := md5.Sum([]byte(rawPath))
	base := stdpath.Base(rawPath)
	name := strings.TrimSuffix(base, stdpath.Ext(base))
	name = sanitize115Name(name)
	return name + "_" + hex.EncodeToString(h[:4]) + ".png"
}

// remoteThumbMiss 远程缩略图"不存在"的内存负缓存，避免每次请求都查询 115
var (
	remoteThumbMissMu sync.Mutex
	remoteThumbMiss   = map[string]time.Time{}
)

const remoteThumbMissTTL = 10 * time.Minute

func remoteThumbMissCheck(rawPath string) bool {
	remoteThumbMissMu.Lock()
	defer remoteThumbMissMu.Unlock()
	t, ok := remoteThumbMiss[rawPath]
	if !ok {
		return false
	}
	if time.Since(t) > remoteThumbMissTTL {
		delete(remoteThumbMiss, rawPath)
		return false
	}
	return true
}

func remoteThumbMissMark(rawPath string) {
	remoteThumbMissMu.Lock()
	remoteThumbMiss[rawPath] = time.Now()
	remoteThumbMissMu.Unlock()
}

func remoteThumbMissClear(rawPath string) {
	remoteThumbMissMu.Lock()
	delete(remoteThumbMiss, rawPath)
	remoteThumbMissMu.Unlock()
}

func remoteThumbPath(addition interface {
	ThumbFolderName() string
}, rawPath string) string {
	return stdpath.Dir(rawPath) + "/" + addition.ThumbFolderName() + "/" + remoteThumbName(rawPath)
}

// folderNameOnly 适配器：只需目录名的场景复用 loadRemoteThumbListing（无需 remote 模式）
type folderNameOnly struct{ folder string }

func (f folderNameOnly) ThumbFolderName() string { return f.folder }

// removeRemoteThumb 删除网盘 _thumbnails 中的单个缩略图文件。
// 与 thumb_store 模式无关（local 模式用户也可能手动上传到网盘，删除时必须同步云端）。
// 先刷新该目录列表再删除：fs.Remove 内部 Get 会命中过期的目录对象缓存，
// 若缓存缺失该文件会判定"对象不存在"而静默跳过（不删除），导致删除后云端残留、
// 生成时误判"网盘已有"而不入队。
func removeRemoteThumb(ctx context.Context, rawPath string) {
	folder := thumbFolderNameForPath(rawPath)
	if folder == "" {
		return
	}
	dir := stdpath.Dir(rawPath)
	// 刷新目录缓存（1 次 115 列表请求）
	_, _ = fs.List(ctx, stdpath.Join(dir, folder), &fs.ListArgs{Refresh: true, NoLog: true})
	full := stdpath.Join(dir, folder, remoteThumbName(rawPath))
	_ = fs.Remove(ctx, full)
}

// tryRestoreRemoteThumb 本地模式视频缩略图未命中时，尝试从网盘 _thumbnails 恢复上传副本，
// 避免"上传→清空本地→重新获取"时重复下载+ffmpeg 生成。恢复成功返回图片字节。
func tryRestoreRemoteThumb(ctx context.Context, rawPath string) ([]byte, bool) {
	if rawPath == "" {
		return nil, false
	}
	dirPath := stdpath.Dir(rawPath)
	folder := thumbFolderNameForPath(rawPath)
	if folder == "" {
		return nil, false
	}
	names := loadRemoteThumbListing(ctx, dirPath, folderNameOnly{folder})
	if len(names) == 0 || !names[remoteThumbName(rawPath)] {
		return nil, false
	}
	remotePath := stdpath.Join(dirPath, folder, remoteThumbName(rawPath))
	obj, err := fs.Get(ctx, remotePath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, false
	}
	link, _, err := fs.Link(ctx, remotePath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		return nil, false
	}
	defer link.Close()
	proxy := thumbProxyForPath(rawPath)
	data, err := downloadRangeBytes(ctx, link, 0, obj.GetSize(), proxy)
	if err != nil {
		return nil, false
	}
	return data, true
}

func remoteThumbCacheGet(rawPath string) ([]byte, bool) {
	remoteThumbCacheMu.Lock()
	defer remoteThumbCacheMu.Unlock()
	e, ok := remoteThumbCache[rawPath]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > remoteThumbCacheTTL {
		delete(remoteThumbCache, rawPath)
		remoteThumbCacheSize -= len(e.data)
		return nil, false
	}
	return e.data, true
}

func remoteThumbCacheSet(rawPath string, data []byte) {
	remoteThumbCacheMu.Lock()
	defer remoteThumbCacheMu.Unlock()
	if old, ok := remoteThumbCache[rawPath]; ok {
		remoteThumbCacheSize -= len(old.data)
	}
	// 超上限时清空最老的一半
	if remoteThumbCacheSize+len(data) > remoteThumbCacheMax {
		type kv struct {
			k string
			t time.Time
		}
		var list []kv
		for k, v := range remoteThumbCache {
			list = append(list, kv{k, v.at})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].t.Before(list[j].t) })
		for i := 0; i < len(list)/2; i++ {
			if e, ok := remoteThumbCache[list[i].k]; ok {
				remoteThumbCacheSize -= len(e.data)
				delete(remoteThumbCache, list[i].k)
			}
		}
	}
	remoteThumbCache[rawPath] = remoteThumbEntry{data: data, at: time.Now()}
	remoteThumbCacheSize += len(data)
}

// uploadThumbRemote 将缩略图上传到视频所在目录的 _thumbnails 文件夹
func uploadThumbRemote(ctx context.Context, rawPath string, addition interface {
	ThumbFolderName() string
}, data []byte) error {
	// 上传限流：与生成共用并发名额，避免多视频同时上传触发网盘风控
	if !thumbAcquire(ctx, 0) {
		return errors.New("thumbnail upload busy")
	}
	defer thumbRelease()
	thumbName := remoteThumbName(rawPath)
	thumbFullPath := remoteThumbPath(addition, rawPath)
	if _, err := fs.Get(ctx, thumbFullPath, &fs.GetArgs{NoLog: true}); err == nil {
		return nil // 已存在
	}
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     thumbName,
			Size:     int64(len(data)),
			Modified: time.Now(),
		},
		Reader: bytes.NewReader(data),
	}
	// fs.PutDirectly 接收完整路径（含挂载前缀），内部自动解析为驱动路径，
	// 避免缩略图文件夹被错误创建到网盘根目录
	dir := stdpath.Dir(rawPath) + "/" + addition.ThumbFolderName()
	return fs.PutDirectly(ctx, dir, s)
}

// serveRemoteVideoThumb 远程模式视频缩略图：内存缓存 → 网盘已有 → 生成并上传网盘
func serveRemoteVideoThumb(c *gin.Context, rawPath string, addition interface {
	ThumbStoreRemote() bool
	ThumbFolderName() string
}) {
	// 1) 内存缓存
	if data, ok := remoteThumbCacheGet(rawPath); ok {
		serveThumbPNG(c, data)
		return
	}
	// 2) 本地磁盘缓存（零 115 API）
	diskPath := thumbCachePath(thumbKindVideo, rawPath)
	if data, err := os.ReadFile(diskPath); err == nil {
		remoteThumbCacheSet(rawPath, data)
		serveThumbPNG(c, data)
		return
	}
	if remoteThumbMissCheck(rawPath) {
		serveThumbPlaceholder(c)
		return
	}
	// 风控中：不调 115 远程查询或下载生成，直接返回占位图（避免加剧风控）
	if blocked, _ := isStorageBlocked(rawPath); blocked {
		serveThumbPlaceholder(c)
		return
	}
	// 排除的视频不生成
	if readThumbExcluded()[rawPath] {
		serveThumbPlaceholder(c)
		return
	}
	// 3) 目录清单判断远程缩略图是否存在（清单由列表时 1 次 API 建立）
	dirPath := stdpath.Dir(rawPath)
	if exists, known := remoteThumbInListing(dirPath, rawPath); known {
		if !exists {
			// 清单明确无缩略图：走生成
			generateAndServeRemote(c, rawPath, addition, diskPath)
			return
		}
		// 清单确认存在：从 115 读取一次，之后磁盘/内存缓存
		remotePath := remoteThumbPath(addition, rawPath)
		if obj, err := fs.Get(c.Request.Context(), remotePath, &fs.GetArgs{NoLog: true}); err == nil {
			if link, _, err := fs.Link(c.Request.Context(), remotePath, model.LinkArgs{Header: thumbLinkHeader()}); err == nil {
				defer link.Close()
				proxy := thumbProxyForPath(rawPath)
				if data, err := downloadRangeBytes(c.Request.Context(), link, 0, obj.GetSize(), proxy); err == nil {
					remoteThumbMissClear(rawPath)
					remoteThumbCacheSet(rawPath, data)
					_ = writeFileAtomic(diskPath, data, 0o666)
					serveThumbPNG(c, data)
					return
				}
			}
		}
		// 读取失败，回退生成
		generateAndServeRemote(c, rawPath, addition, diskPath)
		return
	}
	// 4) 清单未建立：直接查远程（保持兼容），失败则生成
	remotePath := remoteThumbPath(addition, rawPath)
	if obj, err := fs.Get(c.Request.Context(), remotePath, &fs.GetArgs{NoLog: true}); err == nil {
		if link, _, err := fs.Link(c.Request.Context(), remotePath, model.LinkArgs{Header: thumbLinkHeader()}); err == nil {
			defer link.Close()
			proxy := thumbProxyForPath(rawPath)
			if data, err := downloadRangeBytes(c.Request.Context(), link, 0, obj.GetSize(), proxy); err == nil {
				remoteThumbMissClear(rawPath)
				remoteThumbCacheSet(rawPath, data)
				_ = writeFileAtomic(diskPath, data, 0o666)
				thumbRecord(rawPath)
				serveThumbPNG(c, data)
				return
			}
		}
	} else {
		remoteThumbMissMark(rawPath)
	}
	generateAndServeRemote(c, rawPath, addition, diskPath)
}

// generateAndServeRemote 生成缩略图并上传远程、写入本地磁盘缓存
func generateAndServeRemote(c *gin.Context, rawPath string, addition interface {
	ThumbStoreRemote() bool
	ThumbFolderName() string
}, diskPath string) {
	// 风控中 / 排除：返回占位图（不调 115）
	if blocked, _ := isStorageBlocked(rawPath); blocked {
		serveThumbPlaceholder(c)
		return
	}
	if readThumbExcluded()[rawPath] {
		serveThumbPlaceholder(c)
		return
	}
	png, err := generateThumbOnce(thumbKindVideo, rawPath, func() ([]byte, error) {
		if !thumbAcquire(c.Request.Context(), 0) {
			return nil, errors.New("thumbnail generation capacity unavailable")
		}
		defer thumbRelease()
		return generateVideoThumb(c.Request.Context(), rawPath)
	})
	if err != nil {
		if errors.Is(err, errThumbTooLarge) {
			remoteThumbMissMark(rawPath)
			serveThumbPlaceholder(c)
			return
		}
		// 所有生成失败写负缓存，避免反复下载+抽帧（浪费带宽并刺激网盘风控）
		remoteThumbMissMark(rawPath)
		log.Warnf("thumb generate failed [video] %s: %v", rawPath, err)
		serveThumbPlaceholder(c)
		return
	}
	if isBlankThumb(png) {
		remoteThumbMissMark(rawPath)
		log.Warnf("thumb generate blank [video] %s", rawPath)
		serveThumbPlaceholder(c)
		return
	}
	go func() {
		if err := uploadThumbRemote(context.WithoutCancel(c.Request.Context()), rawPath, addition, png); err != nil {
			log.Warnf("thumb upload remote failed %s: %v", rawPath, err)
		}
	}()
	remoteThumbMissClear(rawPath)
	remoteThumbCacheSet(rawPath, png)
	_ = writeFileAtomic(diskPath, png, 0o666)
	thumbRecord(rawPath)
	serveThumbPNG(c, png)
}

// AudioThumb GET /at/*path
// 音频文件缩略图：提取内嵌专辑封面，结果缓存
func AudioThumb(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	startThumbCleanup()
	serveThumb(c, thumbKindAudio, rawPath, func() ([]byte, error) {
		if _, err := ffmpegBin(); err != nil {
			return nil, err
		}
		maxSize := int64(setting.GetInt(conf.ThumbAudioMaxSize, 50*1024*1024))
		link, obj, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{Header: thumbLinkHeader()})
		if err != nil {
			return nil, err
		}
		defer link.Close()
		size := obj.GetSize()
		if size > maxSize {
			return nil, errThumbTooLarge
		}
		tmpFile, err := newThumbTempPath(thumbKindAudio, rawPath, ".tmp.mp3")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmpFile)
		proxy := thumbProxyForPath(rawPath)
		if _, err := downloadRange(c.Request.Context(), link, tmpFile, 0, size, proxy); err != nil {
			return nil, err
		}
		return extractAudioCover(c.Request.Context(), tmpFile)
	})
}

// generateImageThumb 生成图片缩略图（直接请求与预热共用）
func generateImageThumb(ctx context.Context, rawPath string) ([]byte, error) {
	maxSize := int64(setting.GetInt(conf.ThumbImageMaxSize, 20*1024*1024))
	link, obj, err := fs.Link(ctx, rawPath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	size := obj.GetSize()
	if size > maxSize {
		return nil, errThumbTooLarge
	}
	tmpFile, err := newThumbTempPath(thumbKindImage, rawPath, ".tmp.img")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile)
	proxy := thumbProxyForPath(rawPath)
	if _, err := downloadRange(ctx, link, tmpFile, 0, size, proxy); err != nil {
		return nil, err
	}
	return resizeImageFile(tmpFile)
}

// generateCoverThumb 生成目录封面（直接请求与预热共用）
func generateCoverThumb(ctx context.Context, rawPath string) ([]byte, error) {
	// 缩略图文件夹自身不作为封面候选
	if stdpath.Base(rawPath) == thumbFolderNameForPath(rawPath) {
		return nil, errThumbNoCover
	}
	maxSize := int64(setting.GetInt(conf.ThumbImageMaxSize, 20*1024*1024))
	names := strings.Split(setting.GetStr(conf.ThumbCoverNames, "folder.jpg,cover.jpg,thumb.jpg,folder.png,cover.png,thumb.png"), ",")
	objs, err := fs.List(ctx, rawPath, &fs.ListArgs{})
	if err != nil {
		return nil, err
	}
	byName := map[string]bool{}
	for _, obj := range objs {
		byName[strings.ToLower(obj.GetName())] = obj.IsDir()
	}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || byName[name] {
			continue
		}
		coverPath := rawPath + "/" + name
		link, obj, err := fs.Link(ctx, coverPath, model.LinkArgs{Header: thumbLinkHeader()})
		if err != nil || obj.IsDir() {
			continue
		}
		if obj.GetSize() > maxSize {
			link.Close()
			continue
		}
		tmpFile, err := newThumbTempPath(thumbKindCover, rawPath, ".tmp.cover")
		if err != nil {
			link.Close()
			continue
		}
		proxy := thumbProxyForPath(rawPath)
		_, dlErr := downloadRange(ctx, link, tmpFile, 0, obj.GetSize(), proxy)
		link.Close()
		if dlErr != nil {
			_ = os.Remove(tmpFile)
			continue
		}
		defer os.Remove(tmpFile)
		return resizeImageFile(tmpFile)
	}
	return nil, errThumbNoCover
}

// ImageThumb GET /it/*path
// 图片文件缩略图：下载后缩放为 PNG，结果缓存
func ImageThumb(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	startThumbCleanup()
	serveThumb(c, thumbKindImage, rawPath, func() ([]byte, error) {
		return generateImageThumb(c.Request.Context(), rawPath)
	})
}

// CoverThumb GET /ct/*path
// 目录封面：查找目录下的封面文件（folder.jpg 等）并缩放返回，探测结果负缓存
func CoverThumb(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	startThumbCleanup()
	serveThumb(c, thumbKindCover, rawPath, func() ([]byte, error) {
		return generateCoverThumb(c.Request.Context(), rawPath)
	})
}

// startThumbCleanup 启动后台缓存清理（TTL 过期 + 总量上限）
func startThumbCleanup() {
	thumbCleanupOnce.Do(func() {
		go func() {
			t := time.NewTicker(thumbCleanupIt)
			defer t.Stop()
			for range t.C {
				thumbCleanupOnceRun()
			}
		}()
		thumbCleanupOnceRun()
	})
}

func isThumbCacheArtifact(name string) bool {
	if !strings.HasSuffix(name, ".png") {
		return false
	}
	for _, prefix := range []string{thumbKindVideo + "-", thumbKindAudio + "-", thumbKindImage + "-", thumbKindCover + "-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func thumbCleanupOnceRun() {
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	ttl := time.Duration(setting.GetInt(conf.ThumbCacheTTL, 30)) * 24 * time.Hour
	maxSize := int64(setting.GetInt(conf.ThumbCacheMaxSize, 2*1024*1024*1024))
	now := time.Now()
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var files []entry
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".tmp.mp4") || strings.HasSuffix(name, ".tmp.mp3") ||
			strings.HasSuffix(name, ".tmp.img") || strings.HasSuffix(name, ".tmp.cover") {
			if now.Sub(fi.ModTime()) > thumbTmpTTL {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if strings.HasSuffix(name, ".fail") {
			if now.Sub(fi.ModTime()) > thumbFailTTLDuration() {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		// index.jsonl/cloud.jsonl/excluded.jsonl and any future metadata are not cache artifacts.
		if !isThumbCacheArtifact(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if now.Sub(fi.ModTime()) > ttl {
			_ = os.Remove(p)
			continue
		}
		files = append(files, entry{p, fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	if total <= maxSize {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= maxSize {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	// 清理删除了缓存文件后重写索引，只保留缓存仍存在的条目，保证统计与实际一致
	thumbRewriteIndex()
}

// thumbURL 构造指向缩略图接口的 URL
func thumbURL(c *gin.Context, prefix, parent string, obj model.Obj) string {
	fullPath := parent + "/" + obj.GetName()
	thumbURL := common.GetApiUrl(c) + "/" + prefix + utils.EncodePath(fullPath, true)
	thumbURL += "?sign=" + sign.SignPath(fullPath)
	return thumbURL
}

// fillVideoThumb 若对象为视频且无缩略图，构造指向缩略图接口的 URL
func fillVideoThumb(c *gin.Context, parent string, obj model.Obj, thumb string) string {
	if thumb != "" {
		return thumb
	}
	if obj.IsDir() {
		return ""
	}
	if utils.GetFileType(obj.GetName()) != conf.VIDEO {
		return ""
	}
	// remote 模式：异步预载目录缩略图清单（1 次 API/目录，缓存 5 分钟），
	// 使 /vt 读取后续零 API；风控中跳过，避免列表时触发 115 API 加剧风控
	if addition := remoteThumbStore(parent + "/" + obj.GetName()); addition != nil {
		if blocked, _ := isStorageBlocked(parent); !blocked {
			preloadRemoteListing(c.Request.Context(), parent, addition)
		}
	}
	return thumbURL(c, "vt", parent, obj)
}

// fillAudioThumb 若对象为音频且无缩略图，构造指向封面提取接口的 URL
func fillAudioThumb(c *gin.Context, parent string, obj model.Obj, thumb string) string {
	if thumb != "" {
		return thumb
	}
	if obj.IsDir() {
		return ""
	}
	if utils.GetFileType(obj.GetName()) != conf.AUDIO {
		return ""
	}
	return thumbURL(c, "at", parent, obj)
}

// fillImageThumb 若对象为栅格图片且无缩略图，构造指向图片缩略图接口的 URL
func fillImageThumb(c *gin.Context, parent string, obj model.Obj, thumb string) string {
	if thumb != "" {
		return thumb
	}
	if obj.IsDir() {
		return ""
	}
	if utils.GetFileType(obj.GetName()) != conf.IMAGE {
		return ""
	}
	ext := strings.ToLower(utils.Ext(obj.GetName()))
	if ext == ".svg" || ext == ".ico" || ext == ".swf" {
		return ""
	}
	return thumbURL(c, "it", parent, obj)
}

// fillCoverThumb 目录封面：目录在网格视图显示封面图片
func fillCoverThumb(c *gin.Context, parent string, obj model.Obj) string {
	if !obj.IsDir() {
		return ""
	}
	if setting.GetStr(conf.ThumbDirCover, "false") != "true" {
		return ""
	}
	// 缩略图文件夹自身不作为封面候选（避免"无封面/无法抽帧"失败）
	if obj.GetName() == thumbFolderNameForPath(parent+"/"+obj.GetName()) {
		return ""
	}
	return thumbURL(c, "ct", parent, obj)
}
