package handles

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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

// probeVideoDuration 同步探测视频时长（秒），探测失败返回 0
func probeVideoDuration(ctx context.Context, rawPath, apiURL string) float64 {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	if _, err := ffprobeBin(); err != nil {
		return 0
	}
	videoDurMu.Lock()
	if e, ok := videoDurCache[rawPath]; ok {
		if time.Since(e.at) < videoDurCacheTTL {
			videoDurMu.Unlock()
			return e.dur
		}
	}
	if videoDurProbe[rawPath] {
		videoDurMu.Unlock()
		return 0
	}
	videoDurProbe[rawPath] = true
	videoDurMu.Unlock()

	videoDurSem <- struct{}{}
	defer func() { <-videoDurSem }()
	videoDurMu.Lock()
	videoDurProbeN++
	videoDurMu.Unlock()

	url := apiURL + "/d" + utils.EncodePath(rawPath, true) + "?sign=" + sign.SignPath(rawPath)
	cmdCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, videoDurPath,
		"-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", url)
	out, err := cmd.Output()

	videoDurMu.Lock()
	defer videoDurMu.Unlock()
	delete(videoDurProbe, rawPath)
	if err != nil {
		videoDurCache[rawPath] = videoDurEntry{dur: 0, at: time.Now().Add(videoDurCacheTTL - videoDurMissTTL)} // 负缓存 1h
		return 0
	}
	dur, parseErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if parseErr != nil || dur <= 0 {
		videoDurCache[rawPath] = videoDurEntry{dur: 0, at: time.Now().Add(videoDurCacheTTL - videoDurMissTTL)}
		return 0
	}
	videoDurCache[rawPath] = videoDurEntry{dur: dur, at: time.Now()}
	return dur
}

// videoDuration 读取缓存的视频时长（未缓存则异步探测，返回当前值）
func videoDuration(ctx context.Context, rawPath, apiURL string) float64 {
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	videoDurMu.Lock()
	e, ok := videoDurCache[rawPath]
	probing := videoDurProbe[rawPath]
	videoDurMu.Unlock()
	if ok && time.Since(e.at) < videoDurCacheTTL {
		return e.dur
	}
	if !ok && !probing {
		// 异步探测（列表场景不阻塞）
		go func() {
			probeVideoDuration(context.Background(), rawPath, apiURL)
		}()
	}
	return 0
}

// videoDurationSync 同步探测（详情场景）
func videoDurationSync(ctx context.Context, rawPath, apiURL string) float64 {
	return probeVideoDuration(ctx, rawPath, apiURL)
}

var _ = conf.Conf
