package handles

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	stdpath "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
)

// thumbSemMu 动态并发信号量：容量来自设置 thumb_concurrency
var (
	thumbSemMu    sync.Mutex
	thumbSemCount int
)

// thumbAcquire 获取生成并发名额。withTimeout 为 true 时（预热任务）超时即让位，
// 保证浏览器直接请求优先；false 时（直接请求）无限等待。
func thumbAcquire(withTimeout bool) (got bool) {
	limit := setting.GetInt(conf.ThumbConcurrency, 8)
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

func markThumbFailed(kind, rawPath string) {
	_ = os.WriteFile(thumbFailPath(kind, rawPath), nil, 0o666)
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

// downloadRange 从链接读取 [offset, offset+limit) 字节到本地：
// 优先使用驱动提供的 RangeReader（本地/流式驱动），否则用 Range 请求下载
func downloadRange(ctx context.Context, link *model.Link, dstPath string, offset, limit int64) (int64, error) {
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
		client := &http.Client{Timeout: 90 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("download failed: %d %s", resp.StatusCode, resp.Status)
		}
		rc = resp.Body
	}
	f, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, rc)
}

// extractVideoFrame 从本地视频文件抽帧（-ss 3 失败回退 0s）
func extractVideoFrame(localPath string) ([]byte, error) {
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

// extractVideoFrameRemote 通过 ffmpeg HTTP Range 直接远程抽帧，
// 适用于 moov 在文件尾部、本地切片无法解析的场景（只传输所需字节）
func extractVideoFrameRemote(url string, header http.Header) ([]byte, error) {
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
	stream := ffmpeg.Input(url, ffmpeg.KwArgs{"noaccurate_seek": "", "ss": "3", "timeout": "90000000"}).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
		GlobalArgs("-headers", hb.String(), "-loglevel", "error").Silent(true).
		WithOutput(srcBuf, os.Stdout)
	if err := stream.Run(); err != nil {
		return nil, err
	}
	if srcBuf.Len() == 0 {
		return nil, fmt.Errorf("empty output")
	}
	return encodeThumb(srcBuf.Bytes())
}

// extractAudioCover 从本地音频文件提取内嵌封面（无封面时返回 errThumbNoCover）
func extractAudioCover(localPath string) ([]byte, error) {
	srcBuf := bytes.NewBuffer(nil)
	stream := ffmpeg.Input(localPath).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
		GlobalArgs("-map", "0:v:0", "-an", "-loglevel", "error").Silent(true).
		WithOutput(srcBuf, os.Stdout)
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
		common.ErrorStrResp(c, "thumbnail not available", 404)
		return
	}

	if !thumbAcquire(false) {
		common.ErrorStrResp(c, "thumbnail busy", 503)
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
			markThumbFailed(kind, rawPath)
			common.ErrorStrResp(c, "no cover art or cover file found", 404)
			return
		}
		if errors.Is(err, errThumbTooLarge) {
			common.ErrorStrResp(c, "file too large for thumbnail", 404)
			return
		}
		markThumbFailed(kind, rawPath)
		log.Warnf("thumb generate failed [%s] %s: %v", kind, rawPath, err)
		common.ErrorResp(c, err, 500)
		return
	}
	_ = os.WriteFile(cachePath, png, 0o666)
	serveThumbPNG(c, png)
}

// downloadRangeBytes 从链接读取 [offset, offset+limit) 字节返回
func downloadRangeBytes(ctx context.Context, link *model.Link, offset, limit int64) ([]byte, error) {
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
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download failed: %d %s", resp.StatusCode, resp.Status)
		}
		rc = resp.Body
	}
	defer func() {
		if rc != nil {
			_ = rc.Close()
		}
	}()
	return io.ReadAll(io.LimitReader(rc, limit))
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
	data, err := downloadRangeBytes(ctx, link, size-tailLen, tailLen)
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

// generateVideoThumb 生成视频缩略图（直接请求与预热共用）
func generateVideoThumb(ctx context.Context, rawPath string, apiURL string) ([]byte, error) {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	if _, err := ffmpegBin(); err != nil {
		return nil, err
	}
	maxSize := int64(setting.GetInt(conf.ThumbVideoMaxSize, 2*1024*1024*1024))
	link, obj, err := fs.Link(ctx, rawPath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	size := obj.GetSize()
	if size > maxSize {
		return nil, errThumbTooLarge
	}
	remoteURL := apiURL + "/d" + utils.EncodePath(rawPath, true) + "?sign=" + sign.SignPath(rawPath)

	// moov 在文件尾部时本地片段必然无法解析，直接远程抽帧，避免无谓的下载与失败尝试
	if moovAtTail(ctx, link, size, rawPath) {
		return extractVideoFrameRemote(remoteURL, link.Header)
	}

	cachePath := thumbCachePath(thumbKindVideo, rawPath)
	tmpFile := cachePath + ".tmp.mp4"
	defer os.Remove(tmpFile)
	if size <= thumbChunkSize {
		if _, err := downloadRange(ctx, link, tmpFile, 0, size); err != nil {
			return nil, err
		}
		return extractVideoFrame(tmpFile)
	}
	// 下载开头片段（moov 在头部时常见情况）
	if _, err := downloadRange(ctx, link, tmpFile, 0, thumbChunkSize); err != nil {
		return nil, err
	}
	if data, err := extractVideoFrame(tmpFile); err == nil {
		return data, nil
	}
	// moov 位于文件尾部（探测失败或非标准容器）：ffmpeg 直接 HTTP Range 远程抽帧。
	// 走自身 /d 代理接口（服务端已注入驱动 Cookie，不依赖 ffmpeg -headers 传 Cookie，
	// 后者对 115 直链不可靠）；302 直链场景下 -headers 仍保留 Cookie 作兜底。
	if data, err := extractVideoFrameRemote(remoteURL, link.Header); err == nil {
		return data, nil
	}
	// 最后兜底：下载末尾片段（moov 在尾部且本地可解析时有效）
	if _, err := downloadRange(ctx, link, tmpFile, size-thumbChunkSize, size); err != nil {
		return nil, err
	}
	return extractVideoFrame(tmpFile)
}

// prewarmStart 启动预热 worker（低并发细水长流，避免触发网盘 API 风控）
func prewarmStart() {
	prewarmOnce.Do(func() {
		prewarmCh = make(chan thumbPrewarmTask, 2048)
		workers := 2
		for i := 0; i < workers; i++ {
			go prewarmWorker()
		}
	})
}

func prewarmWorker() {
	for task := range prewarmCh {
		cachePath := thumbCachePath(task.kind, task.rawPath)
		if _, err := os.ReadFile(cachePath); err == nil {
			prewarmDone.Store(task.rawPath, struct{}{})
			continue
		}
		if thumbFailed(task.kind, task.rawPath) {
			prewarmDone.Store(task.rawPath, struct{}{})
			continue
		}
		if !thumbAcquire(true) {
			// 并发资源被直接请求占用，让位稍后重试
			time.Sleep(500 * time.Millisecond)
			prewarmCh <- task
			continue
		}
		func() {
			defer thumbRelease()
			// 生成任务硬限时 90s（115 驱动内部请求无超时，网盘风控黑洞时会永久挂起，
			// 必须用 goroutine+select 强制放弃任务，保证 worker 永不卡死）
			done := make(chan []byte, 1)
			errCh := make(chan error, 1)
			go func() {
				png, err := generateVideoThumb(context.Background(), task.rawPath, task.apiURL)
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
			if err != nil {
				// 预热失败不写 fail 标记（可能为网盘风控等临时问题），
				// 长间隔退避重试（风控冻结通常 10-30 分钟，短间隔只会加重风控）
				if task.retry < 3 && !errors.Is(err, errThumbNoCover) && !errors.Is(err, errThumbTooLarge) {
					prewarmDone.Delete(task.rawPath)
					time.Sleep(180 * time.Second)
					task.retry++
					// 重试任务阻塞入队，保证不丢（新任务入队时丢弃自身而非重试任务）
					prewarmCh <- task
					return
				}
				log.Warnf("thumb prewarm failed [%s] %s: %v", task.kind, task.rawPath, err)
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
			prewarmDone.Store(task.rawPath, struct{}{})
		}()
	}
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
		common.ErrorStrResp(c, "thumbnail not available", 404)
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
				if data, err := downloadRangeBytes(c.Request.Context(), link, 0, obj.GetSize()); err == nil {
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
			if data, err := downloadRangeBytes(c.Request.Context(), link, 0, obj.GetSize()); err == nil {
				remoteThumbMissClear(rawPath)
				remoteThumbCacheSet(rawPath, data)
				_ = os.WriteFile(diskPath, data, 0o666)
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
	png, err := generateVideoThumb(c.Request.Context(), rawPath, common.GetApiUrl(c))
	if err != nil {
		if errors.Is(err, errThumbTooLarge) {
			remoteThumbMissMark(rawPath)
			common.ErrorStrResp(c, "file too large for thumbnail", 404)
			return
		}
		// 生成失败写负缓存，避免反复下载+抽帧（浪费带宽并刺激网盘风控）
		remoteThumbMissMark(rawPath)
		log.Warnf("thumb generate failed [video] %s: %v", rawPath, err)
		common.ErrorResp(c, err, 500)
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
		if _, err := downloadRange(c.Request.Context(), link, tmpFile, 0, size); err != nil {
			return nil, err
		}
		return extractAudioCover(tmpFile)
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
	if _, err := downloadRange(ctx, link, tmpFile, 0, size); err != nil {
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
		_, dlErr := downloadRange(ctx, link, tmpFile, 0, obj.GetSize())
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
	// 使 /vt 读取后续零 API
	if addition := remoteThumbStore(parent + "/" + obj.GetName()); addition != nil {
		preloadRemoteListing(c.Request.Context(), parent, addition)
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
