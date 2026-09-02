package handles

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	stdpath "path"
	"strings"
	"sync"
	"time"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Candidate data plane: preview, candidate cache, frame extraction and applying a chosen frame.
// Job registry/queue/cancel lifecycle is intentionally kept in thumb_candidate_tasks.go.

func ThumbView(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		common.ErrorStrResp(c, "invalid path", 400)
		return
	}
	if obj, err := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); err == nil && !obj.IsDir() {
		thumbRememberObject(thumbKindVideo, path, obj)
	}
	serve := func(data []byte) {
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(200, "image/png", data)
	}
	cachePath := thumbCachePath(thumbKindVideo, path)
	if data, err := os.ReadFile(cachePath); err == nil && !isBlankThumb(data) {
		serve(data)
		return
	}
	png, err := generateThumbOnce(thumbKindVideo, path, func() ([]byte, error) {
		return generateVideoThumb(c.Request.Context(), path)
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if len(png) == 0 || isBlankThumb(png) {
		common.ErrorStrResp(c, "该视频无法生成缩略图（生成结果为空白图）", 422)
		return
	}
	_ = writeFileAtomic(thumbCachePath(thumbKindVideo, path), png, 0o666)
	thumbRecord(path)
	serve(png)
}

type ThumbCandidate struct {
	Index int    `json:"index"`
	At    string `json:"at"`
	Png   string `json:"png"`
}

type thumbCandidateFrame struct {
	index int
	at    string
	png   []byte
}

type thumbCandidateCacheEntry struct {
	at               time.Time
	frames           []thumbCandidateFrame
	sheet            []byte
	recommendedIndex int
	riskBlocked      bool
	truncated        bool
}

const (
	thumbCandidateCacheTTL    = 30 * time.Minute
	thumbCandidateCacheMax    = 64
	thumbCandidateFrameGap    = 350 * time.Millisecond
	thumbCandidate115FrameGap = 2 * time.Second
)

var (
	thumbCandidateCacheMu sync.Mutex
	thumbCandidateCache   = make(map[string]thumbCandidateCacheEntry)
)

func thumbCandidateCacheGet(rawPath string) (thumbCandidateCacheEntry, bool) {
	thumbCandidateCacheMu.Lock()
	defer thumbCandidateCacheMu.Unlock()
	entry, ok := thumbCandidateCache[rawPath]
	if !ok {
		return thumbCandidateCacheEntry{}, false
	}
	if time.Since(entry.at) >= thumbCandidateCacheTTL {
		delete(thumbCandidateCache, rawPath)
		return thumbCandidateCacheEntry{}, false
	}
	return entry, true
}

func thumbCandidateCacheDelete(rawPath string) {
	thumbCandidateCacheMu.Lock()
	delete(thumbCandidateCache, rawPath)
	thumbCandidateCacheMu.Unlock()
}

func thumbCandidateCacheSet(rawPath string, entry thumbCandidateCacheEntry) {
	thumbCandidateCacheMu.Lock()
	defer thumbCandidateCacheMu.Unlock()
	thumbCandidateCache[rawPath] = entry
	for len(thumbCandidateCache) > thumbCandidateCacheMax {
		oldestPath := ""
		var oldestAt time.Time
		for path, candidate := range thumbCandidateCache {
			if oldestPath == "" || candidate.at.Before(oldestAt) {
				oldestPath = path
				oldestAt = candidate.at
			}
		}
		if oldestPath == "" {
			break
		}
		delete(thumbCandidateCache, oldestPath)
	}
}

func thumbCandidateResponse(rawPath string, entry thumbCandidateCacheEntry, cached bool) gin.H {
	candidates := make([]ThumbCandidate, 0, len(entry.frames))
	for _, frame := range entry.frames {
		candidates = append(candidates, ThumbCandidate{
			Index: frame.index,
			At:    frame.at,
			Png:   base64.StdEncoding.EncodeToString(frame.png),
		})
	}
	sheet := ""
	if len(entry.sheet) > 0 {
		sheet = base64.StdEncoding.EncodeToString(entry.sheet)
	}
	return gin.H{
		"path": rawPath, "candidates": candidates, "sheet": sheet,
		"recommended_index": entry.recommendedIndex, "cached": cached,
		"risk_blocked": entry.riskBlocked, "truncated": entry.truncated,
	}
}

func isThumbCandidateRiskError(err error) bool { return isThumbRemoteRiskError(err) }

func thumbCandidate115Mount(rawPath string) string {
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil || storage == nil || storage.GetStorage() == nil {
		return ""
	}
	info := storage.GetStorage()
	if info.Driver != "115 Cloud" && info.Driver != "115 Share" {
		return ""
	}
	return info.MountPath
}

func runThumbCandidateJob(job *thumbCandidateJob, parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 150*time.Second)
	defer cancel()
	select {
	case thumbCandidateGate <- struct{}{}:
		defer func() { <-thumbCandidateGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	thumbCandidateActive.Store(true)
	defer thumbCandidateActive.Store(false)
	for !thumbGenerationAdmission.TryLock() {
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer thumbGenerationAdmission.Unlock()
	if !thumbAcquire(ctx, 2*time.Second) {
		return errors.New("生成并发已满，请稍后再试")
	}
	defer thumbRelease()

	rawPath := job.Path
	storageMount := thumbCandidate115Mount(rawPath)
	frameGap := thumbCandidateFrameGap
	if storageMount != "" {
		frameGap = thumbCandidate115FrameGap
	}
	link, obj, err := fs.Link(ctx, rawPath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		if storageMount != "" && isThumbCandidateRiskError(err) {
			driver115pkg.MarkStorageError(storageMount, err)
		}
		return err
	}
	defer link.Close()
	thumbRememberObject(thumbKindVideo, rawPath, obj)
	remoteURL, remoteHeader, remoteProxy, sourceCleanup, err := thumbFFmpegSource(
		ctx, rawPath, link, obj.GetSize(), thumbProxyForPath(rawPath),
	)
	if err != nil {
		return err
	}
	defer sourceCleanup()

	var positions []string
	if obj.GetSize() > thumbProbeMinSize {
		if duration := probeVideoDuration(ctx, rawPath); duration > 0 {
			for i := 1; i <= videoContactSheetColumns*videoContactSheetRows; i++ {
				positions = append(positions, fmt.Sprintf("%.1f", duration*float64(i)/10.0))
			}
		}
	}
	if len(positions) == 0 {
		positions = []string{"3", "10", "30", "60", "120", "300", "600", "1800", "3600"}
	}
	job.mu.Lock()
	job.State = "running"
	job.Total = len(positions)
	job.mu.Unlock()

	framesAtPosition := make([][]byte, len(positions))
	frames := make([]thumbCandidateFrame, 0, len(positions))
	recommendedIndex := 0
	bestScore := 0.0
	riskBlocked, truncated := false, false
	for i, seconds := range positions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			timer := time.NewTimer(frameGap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if blocked, _ := isStorageBlocked(rawPath); blocked {
			riskBlocked, truncated = true, true
			break
		}
		data, frameErr := extractVideoFrameAt(ctx, remoteURL, remoteHeader, remoteProxy, seconds)
		job.mu.Lock()
		job.Done = i + 1
		job.mu.Unlock()
		if frameErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			if storageMount == "" {
				continue
			}
			truncated = true
			if isThumbCandidateRiskError(frameErr) {
				riskBlocked = true
				driver115pkg.MarkStorageError(storageMount, frameErr)
			}
			break
		}
		png, encodeErr := encodeThumb(data)
		if encodeErr != nil || isBlankThumb(png) {
			continue
		}
		framesAtPosition[i] = png
		score := scoreVideoThumb(png)
		frames = append(frames, thumbCandidateFrame{index: i + 1, at: seconds, png: png})
		if recommendedIndex == 0 || score > bestScore {
			recommendedIndex, bestScore = i+1, score
		}
	}

	var sheet []byte
	if len(frames) > 0 {
		sheet, err = buildVideoContactSheet(framesAtPosition)
		if err != nil {
			log.Warnf("thumb candidate contact sheet failed %s: %v", rawPath, err)
			sheet = nil
		}
	}
	entry := thumbCandidateCacheEntry{
		at: time.Now(), frames: frames, sheet: sheet, recommendedIndex: recommendedIndex,
		riskBlocked: riskBlocked, truncated: truncated,
	}
	thumbCandidateCacheSet(rawPath, entry)
	job.mu.Lock()
	job.entry = entry
	job.State = "succeeded"
	job.mu.Unlock()
	return nil
}

type ThumbApplyCandidateReq struct {
	Path string `json:"path"`
	Png  string `json:"png"`
}

func ThumbApplyCandidate(c *gin.Context) {
	var req ThumbApplyCandidateReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	rawPath := req.Path
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	png, err := base64.StdEncoding.DecodeString(req.Png)
	if err != nil || len(png) == 0 || rawPath == "" {
		common.ErrorStrResp(c, "invalid data", 400)
		return
	}
	ctx := c.Request.Context()
	if blocked, _ := isStorageBlocked(rawPath); blocked {
		common.ErrorStrResp(c, "115 网盘正在风控保护中，暂时不要保存缩略图，请稍后再试", 429)
		return
	}
	if obj, err := fs.Get(ctx, rawPath, &fs.GetArgs{NoLog: true}); err == nil && !obj.IsDir() {
		thumbRememberObject(thumbKindVideo, rawPath, obj)
	}
	if err := writeFileAtomic(thumbCachePath(thumbKindVideo, rawPath), png, 0o666); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	thumbRecord(rawPath)
	clearThumbFailure(thumbKindVideo, rawPath)
	if folder := thumbFolderNameForPath(rawPath); folder != "" {
		removeRemoteThumb(ctx, rawPath)
		if err := uploadThumbRemote(ctx, rawPath, folderNameOnly{folder}, png); err != nil {
			log.Warnf("thumb apply candidate upload failed %s: %v", rawPath, err)
		}
		thumbCloudRecord(rawPath)
		remoteThumbCacheSet(rawPath, png)
	}
	thumbListingInvalidate(stdpath.Dir(rawPath))
	thumbStatsInvalidate()
	prewarmDone.Delete(rawPath)
	common.SuccessResp(c, gin.H{"path": rawPath})
}
