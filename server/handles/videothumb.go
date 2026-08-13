package handles

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

var (
	videoThumbSem   = make(chan struct{}, 4)
	videoThumbOnce  sync.Once
	videoThumbCache string
)

const thumbCacheDir = "thumb_cache"

func videoThumbDir() string {
	videoThumbOnce.Do(func() {
		videoThumbCache = filepath.Join(conf.Conf.TempDir, thumbCacheDir)
		_ = os.MkdirAll(videoThumbCache, 0o755)
	})
	return videoThumbCache
}

func videoThumbCachePath(rawPath string) string {
	h := md5.Sum([]byte(rawPath))
	return filepath.Join(videoThumbDir(), hex.EncodeToString(h[:])+".png")
}

// downloadVideoChunk 用下载凭证 header 下载视频前若干字节到本地
func downloadVideoChunk(url string, header http.Header, dstPath string, limit int64) (int64, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header = header.Clone()
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed: %d %s", resp.StatusCode, resp.Status)
	}
	f, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, resp.Body)
}

// extractVideoFrameLocal 从本地视频文件抽帧（若失败回退到 0s）
func extractVideoFrameLocal(localPath string) ([]byte, error) {
	extract := func(ss string) ([]byte, error) {
		srcBuf := bytes.NewBuffer(nil)
		var inputArgs []string
		if ss != "" {
			inputArgs = append(inputArgs, "-ss", ss)
		}
		stream := ffmpeg.Input(localPath, ffmpeg.KwArgs{"noaccurate_seek": ""}).
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

func encodeThumb(mjpeg []byte) ([]byte, error) {
	img, err := imaging.Decode(bytes.NewReader(mjpeg), imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	thumbImg := imaging.Resize(img, 288, 0, imaging.Lanczos)
	var buf bytes.Buffer
	if err = imaging.Encode(&buf, thumbImg, imaging.PNG); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// VideoThumb GET /vt/*path
// 视频文件缩略图：下载视频片段后本地 ffmpeg 抽帧，结果缓存
func VideoThumb(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	cachePath := videoThumbCachePath(rawPath)
	if data, err := os.ReadFile(cachePath); err == nil {
		c.Data(200, "image/png", data)
		return
	}

	videoThumbSem <- struct{}{}
	defer func() { <-videoThumbSem }()

	// 再次检查缓存（可能并发已生成）
	if data, err := os.ReadFile(cachePath); err == nil {
		c.Data(200, "image/png", data)
		return
	}

	link, _, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
		Header: http.Header{
			"User-Agent": []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
		},
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	defer link.Close()

	// 下载视频前 8MB（包含 moov 元数据与开头帧）
	tmpFile := cachePath + ".tmp.mp4"
	defer os.Remove(tmpFile)
	const chunkSize = 8 * 1024 * 1024
	if _, err := downloadVideoChunk(link.URL, link.Header, tmpFile, chunkSize); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	png, err := extractVideoFrameLocal(tmpFile)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	_ = os.WriteFile(cachePath, png, 0o666)
	c.Data(200, "image/png", png)
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
	fullPath := parent + "/" + obj.GetName()
	thumbURL := common.GetApiUrl(c) + "/vt" + utils.EncodePath(fullPath, true)
	thumbURL += "?sign=" + sign.SignPath(fullPath)
	return thumbURL
}
