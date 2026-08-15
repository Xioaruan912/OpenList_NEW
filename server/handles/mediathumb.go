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

	errThumbTooLarge = errors.New("file too large for thumbnail")
	errThumbNoCover  = errors.New("no cover art or cover file found")
	errThumbBlank    = errors.New("blank thumbnail")
)

// isPermanentThumbError 判断生成失败是否属于"永久性"（重试无意义）：
// 115 文件级拦截（403/pmt）、下载被拒等。这类错误写失败标记、不重试，避免长时间空转。
func isPermanentThumbError(err error) bool {
	if err == nil {
		return false
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

// thumbSemMu 动态并发信号量：容量来自设置 thumb_concurrency
var (
	thumbSemMu    sync.Mutex
	thumbSemCount int
)

// thumbAcquire 获取生成并发名额。withTimeout 为 true 时（预热任务）超时即让位，
// 保证浏览器直接请求优先；false 时（直接请求）无限等待。
func thumbAcquire(withTimeout bool) (got bool) {
	limit := thumbGenPower().AcquireLimit
	if limit < 1 {
		limit = 1
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		thumbSemMu.Lock()
		if thumbSemCount < limit {
			thumbSemCount++
			thumbSemMu.Unlock()
			return true
		}
		thumbSemMu.Unlock()
		if withTimeout && time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
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
	apiURL  string
	retry   int
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
	_ = os.WriteFile(thumbFailPath(kind, rawPath), data, 0o666)
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

// thumbProxyForPath 解析缩略图请求代理：
//  1. 用户在缩略图页选择的代理节点（off/auto/manual，manual 节点风控时自动切健康节点）
//  2. 存储级显式代理 / 全局 proxy_address（模式为 off 时走这里）
//
// 返回 (代理地址, 代理节点ID)；nodeID=0 表示未走代理节点
func thumbProxyForPath(rawPath string) (string, uint) {
	if thumbProxyMode() != thumbProxyModeOff {
		if addr, nodeID := resolveThumbProxy(); addr != "" {
			return addr, nodeID
		}
	}
	if rawPath != "" {
		storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
		if err == nil {
			if p, ok := storage.GetAddition().(interface{ GetProxy() string }); ok {
				if px := p.GetProxy(); px != "" {
					return px, 0
				}
			}
		}
	}
	return conf.Conf.ProxyAddress, 0
}

// countingReadCloser 统计通过代理节点读取的字节数
type countingReadCloser struct {
	rc io.ReadCloser
	n  int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }

// newThumbHTTPClient 构建缩略图下载用的 HTTP client，proxy 非空时走指定代理
func newThumbHTTPClient(proxy string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify},
	}
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

// downloadRange 从链接读取 [offset, offset+limit) 字节到本地：
// 优先使用驱动提供的 RangeReader（本地/流式驱动），否则用 Range 请求下载（proxy 非空时走代理）。
// nodeID 非 0 时统计该代理节点的接收字节（OpenList 侧"走代理的流量"），失败触发风控自动切换。
func downloadRange(ctx context.Context, link *model.Link, dstPath string, offset, limit int64, proxy string, nodeID uint) (int64, error) {
	// 连接计数：仅在通过代理节点发起 HTTP 请求时
	useNode := nodeID != 0 && link.RangeReader == nil
	if useNode {
		proxyConnAdd(nodeID)
		defer proxyConnDel(nodeID)
	}
	var (
		rc  io.ReadCloser
		err error
	)
	if link.RangeReader != nil {
		rc, err = link.RangeReader.RangeRead(ctx, http_range.Range{Start: offset, Length: limit})
		if err != nil {
			return 0, err
		}
		defer rc.Close()
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
		if err != nil {
			return 0, err
		}
		req.Header = link.Header.Clone()
		if req.Header == nil {
			req.Header = http.Header{}
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1))
		client := newThumbHTTPClient(proxy, 90*time.Second)
		resp, err := client.Do(req)
		if err != nil {
			recordProxyFailure(nodeID)
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			recordProxyFailure(nodeID)
			return 0, fmt.Errorf("download failed: %d %s", resp.StatusCode, resp.Status)
		}
		rc = resp.Body
	}
	if useNode {
		rc = &countingReadCloser{rc: rc}
	}
	f, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, cpErr := io.Copy(f, rc)
	if cpErr != nil {
		recordProxyFailure(nodeID)
		return n, cpErr
	}
	if useNode {
		if c := rc.(*countingReadCloser); c.n > 0 {
			recordProxyUse(nodeID, c.n, 0)
			recordProxySuccess(nodeID)
		}
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
			Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
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
	var data []byte
	var err error
	if data, err = extract("3"); err == nil {
		return encodeThumb(data)
	}
	if data, err = extract(""); err == nil {
		return encodeThumb(data)
	}
	return nil, err
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

// extractVideoFrameAt 通过 ffmpeg HTTP Range 从远程 URL 指定时间点抽一帧（原始 mjpeg 字节）
func extractVideoFrameAt(ctx context.Context, url string, header http.Header, ss string) ([]byte, error) {
	srcBuf := bytes.NewBuffer(nil)
	var hb strings.Builder
	for k, vs := range header {
		for _, v := range vs {
			hb.WriteString(k)
			hb.WriteString(": ")
			hb.WriteString(v)
			hb.WriteString("\r\n")
		}
	}
	hb.WriteString("\r\n")
	kwargs := ffmpeg.KwArgs{"noaccurate_seek": "", "timeout": "20000000"}
	if ss != "" {
		kwargs["ss"] = ss
	}
	stream := ffmpeg.Input(url, kwargs).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
		GlobalArgs("-headers", hb.String(), "-loglevel", "error").Silent(true).
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

// extractVideoFrameRemote 通过 ffmpeg HTTP Range 直接远程抽帧（3s 处单帧缩略图），
// 适用于 moov 在文件尾部、本地切片无法解析的场景（只传输所需字节）
func extractVideoFrameRemote(ctx context.Context, url string, header http.Header) ([]byte, error) {
	data, err := extractVideoFrameAt(ctx, url, header, "3")
	if err != nil {
		return nil, err
	}
	return encodeThumb(data)
}

// generateVideoAdaptiveFrame 长视频单帧缩略图（1x1）：按"中间优先"依次尝试远程 seek
// 深度 50%→30%→15%→7%→3%，取第一个成功的帧（越深越接近中间内容）。
// 115 对大文件深偏移 Range 常返回 403（每次失败 ~0.5s 快速跳过），
// 单帧+自适应比多帧网格更快更可靠。
func generateVideoAdaptiveFrame(ctx context.Context, url string, header http.Header, duration float64) ([]byte, error) {
	if duration <= 0 {
		return nil, errors.New("invalid duration")
	}
	for _, ratio := range []float64{0.5, 0.3, 0.15, 0.07, 0.03} {
		data, err := extractVideoFrameAt(ctx, url, header, fmt.Sprintf("%.2f", duration*ratio))
		if err != nil {
			continue
		}
		return encodeThumb(data)
	}
	return nil, errors.New("no frame extractable at any depth")
}

// extractAudioCover 从本地音频文件提取内嵌封面（无封面时返回 errThumbNoCover）
func extractAudioCover(ctx context.Context, localPath string) ([]byte, error) {
	srcBuf := bytes.NewBuffer(nil)
	stream := ffmpeg.Input(localPath).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
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
	defer thumbIndexMu.Unlock()
	f, err := os.OpenFile(thumbIndexPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
	// 索引超过 50000 行时截断重写（仅保留缓存文件仍存在的条目）
	if fi, err := os.Stat(thumbIndexPath()); err == nil && fi.Size() > 4*1024*1024 {
		thumbRewriteIndex()
	}
}

// thumbRewriteIndex 重写索引：只保留缓存文件仍存在的条目
func thumbRewriteIndex() {
	lines := readThumbIndex()
	f, err := os.Create(thumbIndexPath())
	if err != nil {
		return
	}
	defer f.Close()
	for _, p := range lines {
		h := thumbHash(p)
		if _, err := os.Stat(filepath.Join(thumbDir(), "video-"+h+".png")); err == nil {
			_, _ = f.WriteString(fmt.Sprintf(`{"path":%s,"at":""}%s`, strconv.Quote(p), "\n"))
		}
	}
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

	if !thumbAcquire(false) {
		// 并发占用：返回占位图（不写负缓存，下次有机会自动重试）
		serveThumbPlaceholder(c)
		return
	}
	defer thumbRelease()

	if data, err := os.ReadFile(cachePath); err == nil {
		serveThumbPNG(c, data)
		return
	}

	png, err := generate()
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
	_ = os.WriteFile(cachePath, png, 0o666)
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

// downloadRangeBytes 从链接读取 [offset, offset+limit) 字节返回
// nodeID 非 0 时统计该代理节点的接收字节，失败触发风控自动切换
func downloadRangeBytes(ctx context.Context, link *model.Link, offset, limit int64, proxy string, nodeID uint) ([]byte, error) {
	useNode := nodeID != 0 && link.RangeReader == nil
	if useNode {
		proxyConnAdd(nodeID)
		defer proxyConnDel(nodeID)
	}
	var (
		rc  io.ReadCloser
		err error
	)
	if link.RangeReader != nil {
		rc, err = link.RangeReader.RangeRead(ctx, http_range.Range{Start: offset, Length: limit})
		if err != nil {
			return nil, err
		}
		defer rc.Close()
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header = link.Header.Clone()
		if req.Header == nil {
			req.Header = http.Header{}
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1))
		client := newThumbHTTPClient(proxy, 30*time.Second)
		resp, err := client.Do(req)
		if err != nil {
			recordProxyFailure(nodeID)
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			recordProxyFailure(nodeID)
			return nil, fmt.Errorf("download failed: %d %s", resp.StatusCode, resp.Status)
		}
		rc = resp.Body
	}
	defer func() {
		if rc != nil {
			_ = rc.Close()
		}
	}()
	var cr *countingReadCloser
	if useNode {
		cr = &countingReadCloser{rc: rc}
		rc = cr
	}
	data, rdErr := io.ReadAll(io.LimitReader(rc, limit))
	if rdErr != nil {
		recordProxyFailure(nodeID)
		return nil, rdErr
	}
	if useNode && cr.n > 0 {
		recordProxyUse(nodeID, cr.n, 0)
		recordProxySuccess(nodeID)
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
func moovAtTail(ctx context.Context, link *model.Link, size int64, rawPath string) bool {
	moovCacheMu.Lock()
	if e, ok := moovCache[rawPath]; ok && time.Since(e.at) < moovCacheTTL {
		moovCacheMu.Unlock()
		return e.atTail
	}
	moovCacheMu.Unlock()
	tailLen := int64(64 * 1024)
	if size < tailLen {
		return false
	}
	proxy, proxyNode := thumbProxyForPath(rawPath)
	data, err := downloadRangeBytes(ctx, link, size-tailLen, tailLen, proxy, proxyNode)
	if err != nil {
		return false
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
	return atTail
}

// generateVideoThumb 生成视频缩略图（直接请求与预热共用）。
// 若经代理下载/抽帧得到空白图（代理中继损坏视频字节的典型表现），
// 自动回退直连重新生成一次。
func generateVideoThumb(ctx context.Context, rawPath string, apiURL string) ([]byte, error) {
	png, err := generateVideoThumbInner(ctx, rawPath, apiURL, true)
	if err == nil && isBlankThumb(png) {
		log.Warnf("thumb blank via proxy, retry direct: %s", rawPath)
		if png2, err2 := generateVideoThumbInner(ctx, rawPath, apiURL, false); err2 == nil {
			return png2, nil
		}
	}
	return png, err
}

// generateVideoThumbInner 生成视频缩略图。useProxy=false 时跳过代理直连下载。
func generateVideoThumbInner(ctx context.Context, rawPath string, apiURL string, useProxy bool) ([]byte, error) {
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
	proxy, proxyNode := thumbProxyForPath(rawPath)
	if !useProxy {
		proxy, proxyNode = "", 0
	}
	size := obj.GetSize()
	// 0 = 不限大小（缩略图只下载开头/末尾 3MB 片段或远程抽帧，不下载整文件，大文件也安全）
	if maxSize > 0 && size > maxSize {
		return nil, errThumbTooLarge
	}
	remoteURL := apiURL + "/d" + utils.EncodePath(rawPath, true) + "?sign=" + sign.SignPath(rawPath)

	// moov 在文件尾部时本地片段必然无法解析；长视频探测时长后取"中间单帧"（1x1），
	// 短视频直接远程抽 3s 单帧
	if moovAtTail(ctx, link, size, rawPath) {
		if size > thumbProbeMinSize {
			if dur := probeVideoDuration(ctx, rawPath, apiURL); dur > thumbMosaicLongSec {
				if data, err := generateVideoAdaptiveFrame(ctx, remoteURL, link.Header, dur); err == nil {
					return data, nil
				}
			}
		}
		return extractVideoFrameRemote(ctx, remoteURL, link.Header)
	}

	// 长视频：探测时长后取"中间单帧"（1x1，中间内容）；失败降级本地头部抽帧
	if size > thumbProbeMinSize {
		if dur := probeVideoDuration(ctx, rawPath, apiURL); dur > thumbMosaicLongSec {
			if data, err := generateVideoAdaptiveFrame(ctx, remoteURL, link.Header, dur); err == nil {
				return data, nil
			}
			log.Debugf("middle frame failed for %s, fallback to head chunk", rawPath)
		}
	}

	cachePath := thumbCachePath(thumbKindVideo, rawPath)
	tmpFile := cachePath + ".tmp.mp4"
	defer os.Remove(tmpFile)
	if size <= thumbChunkSize {
		if _, err := downloadRange(ctx, link, tmpFile, 0, size, proxy, proxyNode); err != nil {
			return nil, err
		}
		return extractVideoFrame(ctx, tmpFile)
	}
	// 下载开头片段（moov 在头部时常见情况；片段不够（大 moov）自动加大重试）
	if data, ok := thumbExtractRange(ctx, link, tmpFile, 0, size, proxy, proxyNode); ok {
		return data, nil
	}
	// moov 位于文件尾部（探测失败或非标准容器）：ffmpeg 直接 HTTP Range 远程抽帧。
	// 走自身 /d 代理接口（服务端已注入驱动 Cookie，不依赖 ffmpeg -headers 传 Cookie，
	// 后者对 115 直链不可靠）；302 直链场景下 -headers 仍保留 Cookie 作兜底。
	if data, err := extractVideoFrameRemote(ctx, remoteURL, link.Header); err == nil {
		return data, nil
	}
	// 最后兜底：下载末尾片段（moov 在尾部且本地可解析时有效；同样自动加大片段）
	if data, ok := thumbExtractRange(ctx, link, tmpFile, size-thumbChunkSize, size, proxy, proxyNode); ok {
		return data, nil
	}
	return nil, errors.New("unable to extract any video frame")
}

// thumbExtractRange 下载 [start, start+chunk) 片段并抽帧；抽帧失败自动加大片段。
// 大 moov 的视频（样本表很大）需要更大的头部/尾部片段才能解析；
// 大文件直接从更大片段开始，避免小片段反复失败。
func thumbExtractRange(ctx context.Context, link *model.Link, tmpFile string, start, size int64, proxy string, nodeID uint) ([]byte, bool) {
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
		if _, err := downloadRange(ctx, link, tmpFile, start, limit, proxy, nodeID); err != nil {
			continue
		}
		if data, err := extractVideoFrame(ctx, tmpFile); err == nil {
			return data, true
		}
	}
	return nil, false
}

// thumbGenPower 返回生成强度参数。当前不做任何节流约束：以最大速度生成
// （8 个 worker、无批间/批内间隔、并发名额上限放宽），不再做 115 风控限速。
type thumbGenPowerCfg struct {
	Workers       int
	BatchInterval time.Duration
	AcquireLimit  int
	FrameInterval time.Duration
	TaskInterval  time.Duration // 批内每个任务处理完后的间隔（降低请求/下载频率）
	EnqueueMax    int
}

func thumbGenPower() thumbGenPowerCfg {
	return thumbGenPowerCfg{Workers: 8, BatchInterval: 0, AcquireLimit: 64, FrameInterval: 0, TaskInterval: 0, EnqueueMax: 100000}
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
	// 115 风控中不下载视频生成缩略图（避免加剧风控）：
	// 放回队列尾部并短暂让位，不阻塞其他存储（如 Onedrive）的任务处理
	if blocked, _ := isStorageBlocked(task.rawPath); blocked {
		prewarmCh <- task
		time.Sleep(2 * time.Second)
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
	if !thumbAcquire(true) {
		// 并发资源被直接请求占用，让位稍后重试
		time.Sleep(500 * time.Millisecond)
		prewarmCh <- task
		return
	}
	atomic.AddInt32(&thumbActiveWorkers, 1)
	defer atomic.AddInt32(&thumbActiveWorkers, -1)
	defer thumbRelease()
	epoch := atomic.LoadInt64(&thumbGenEpoch)
	genCtx, cancel := context.WithCancel(context.Background())
	thumbActiveTrack(task.rawPath, true)
	thumbActiveCancelAdd(task.rawPath, cancel)
	defer func() {
		thumbActiveTrack(task.rawPath, false)
		thumbActiveCancelDel(task.rawPath)
	}()
	// 生成任务硬限时 90s（115 驱动内部请求无超时，网盘风控黑洞时会永久挂起，
	// 必须用 goroutine+select 强制放弃任务，保证 worker 永不卡死）
	done := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		png, err := generateVideoThumb(genCtx, task.rawPath, task.apiURL)
		if err != nil {
			errCh <- err
			return
		}
		done <- png
	}()
	var png []byte
	var err error
	select {
	case png = <-done:
	case err = <-errCh:
	case <-time.After(90 * time.Second):
		err = fmt.Errorf("thumb generation timeout (90s)")
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
		// 生成失败不写 fail 标记（可能为网盘风控等临时问题），
		// 长间隔退避重试（风控冻结通常 10-30 分钟，短间隔只会加重风控）
		if task.retry < 3 {
			prewarmDone.Delete(task.rawPath)
			time.Sleep(180 * time.Second)
			task.retry++
			// 重试任务阻塞入队，保证不丢（新任务入队时丢弃自身而非重试任务）
			prewarmCh <- task
			return
		}
		log.Warnf("thumb prewarm failed [%s] %s: %v", task.kind, task.rawPath, err)
		markThumbFailed(task.kind, task.rawPath, thumbFailReason(err))
		prewarmDone.Store(task.rawPath, struct{}{})
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
		if err := uploadThumbRemote(context.Background(), task.rawPath, addition, png); err != nil {
			log.Warnf("thumb prewarm upload remote failed %s: %v", task.rawPath, err)
		}
		remoteThumbCacheSet(task.rawPath, png)
		_ = os.WriteFile(cachePath, png, 0o666)
	} else {
		_ = os.WriteFile(cachePath, png, 0o666)
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

// prewarmEnqueue 入队预热任务（去重）
func prewarmEnqueue(kind, rawPath, apiURL string) {
	prewarmStart()
	if _, done := prewarmDone.Load(rawPath); done {
		return
	}
	prewarmDone.Store(rawPath, struct{}{})
	select {
	case prewarmCh <- thumbPrewarmTask{kind: kind, rawPath: rawPath, apiURL: apiURL}:
	default:
		// 队列满：清除去重标记，30s 后重试入队
		prewarmDone.Delete(rawPath)
		go func() {
			time.Sleep(30 * time.Second)
			prewarmEnqueue(kind, rawPath, apiURL)
		}()
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
	apiURL := common.GetApiUrl(c)
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		if utils.GetFileType(obj.GetName()) != conf.VIDEO {
			continue
		}
		prewarmEnqueue(thumbKindVideo, parent+"/"+obj.GetName(), apiURL)
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
		return generateVideoThumb(c.Request.Context(), rawPath, common.GetApiUrl(c))
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
	if len(name) > 40 {
		name = name[:40]
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
	if !thumbAcquire(false) {
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
				proxy, proxyNode := thumbProxyForPath(rawPath)
				if data, err := downloadRangeBytes(c.Request.Context(), link, 0, obj.GetSize(), proxy, proxyNode); err == nil {
					remoteThumbMissClear(rawPath)
					remoteThumbCacheSet(rawPath, data)
					_ = os.WriteFile(diskPath, data, 0o666)
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
			proxy, proxyNode := thumbProxyForPath(rawPath)
			if data, err := downloadRangeBytes(c.Request.Context(), link, 0, obj.GetSize(), proxy, proxyNode); err == nil {
				remoteThumbMissClear(rawPath)
				remoteThumbCacheSet(rawPath, data)
				_ = os.WriteFile(diskPath, data, 0o666)
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
	png, err := generateVideoThumb(c.Request.Context(), rawPath, common.GetApiUrl(c))
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
	_ = os.WriteFile(diskPath, png, 0o666)
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
		tmpFile := thumbCachePath(thumbKindAudio, rawPath) + ".tmp.mp3"
		defer os.Remove(tmpFile)
		proxy, proxyNode := thumbProxyForPath(rawPath)
		if _, err := downloadRange(c.Request.Context(), link, tmpFile, 0, size, proxy, proxyNode); err != nil {
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
	tmpFile := thumbCachePath(thumbKindImage, rawPath) + ".tmp.img"
	defer os.Remove(tmpFile)
	proxy, proxyNode := thumbProxyForPath(rawPath)
	if _, err := downloadRange(ctx, link, tmpFile, 0, size, proxy, proxyNode); err != nil {
		return nil, err
	}
	return resizeImageFile(tmpFile)
}

// generateCoverThumb 生成目录封面（直接请求与预热共用）
func generateCoverThumb(ctx context.Context, rawPath string) ([]byte, error) {
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
		tmpFile := thumbCachePath(thumbKindCover, rawPath) + ".tmp.cover"
		proxy, proxyNode := thumbProxyForPath(rawPath)
		_, dlErr := downloadRange(ctx, link, tmpFile, 0, obj.GetSize(), proxy, proxyNode)
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
	if setting.GetStr(conf.ThumbDirCover, "true") != "true" {
		return ""
	}
	return thumbURL(c, "ct", parent, obj)
}
