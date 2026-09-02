package handles

import (
	"context"
	stdpath "path"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// Low-frequency status/control plane. Expensive remote truth is refreshed asynchronously; queue
// controls manipulate the task plane without mixing that state into CRUD handlers.

var (
	thumbAggMu         sync.Mutex
	thumbAgg           struct{ cached, local, cloud int }
	thumbAggAt         time.Time
	thumbAggRefreshing atomic.Bool
)

const thumbAggTTL = 30 * time.Second

func refreshThumbAgg(ctx context.Context) {
	localFiles, _, _ := thumbCacheStats()
	cloudFiles, overlap := thumbCloudStats(ctx)
	thumbAggMu.Lock()
	thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = localFiles+cloudFiles-overlap, localFiles, cloudFiles
	thumbAggAt = time.Now()
	thumbAggMu.Unlock()
}

func knownThumbAgg() (cached, cloud int) {
	union := map[string]struct{}{}
	for _, path := range readThumbIndex() {
		union[path] = struct{}{}
	}
	cloudSet := readThumbCloudIndex()
	for path := range cloudSet {
		union[path] = struct{}{}
	}
	return len(union), len(cloudSet)
}

func refreshThumbAggAsync() {
	if !thumbAggRefreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer thumbAggRefreshing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		refreshThumbAgg(ctx)
	}()
}

func ThumbStatus(c *gin.Context) {
	localCount, _, totalSize := thumbCacheStats()
	fails := listThumbFails()
	var cachedFiles, localFiles, cloudFiles int
	thumbAggMu.Lock()
	if time.Since(thumbAggAt) < thumbAggTTL {
		cachedFiles, localFiles, cloudFiles = thumbAgg.cached, thumbAgg.local, thumbAgg.cloud
		thumbAggMu.Unlock()
	} else {
		thumbAggMu.Unlock()
		cachedFiles, cloudFiles = knownThumbAgg()
		localFiles = localCount
		thumbAggMu.Lock()
		thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = cachedFiles, localFiles, cloudFiles
		thumbAggAt = time.Now()
		thumbAggMu.Unlock()
		refreshThumbAggAsync()
	}

	status := gin.H{
		"cache_dir": thumbDir(), "cached_files": cachedFiles, "local_files": localFiles,
		"cloud_files": cloudFiles, "fail_markers": len(fails), "cache_size": totalSize,
		"prewarm_enabled": setting.GetStr(conf.ThumbPrewarm, "true") == "true",
		"queue_paused":    thumbQueuePaused.Load(),
		"auto_upload":     setting.GetStr(conf.ThumbAutoUpload, "false") == "true",
	}
	status["prewarm_queued"] = thumbPrewarmQueued()
	power := thumbGenPower()
	status["worker_concurrency"] = power.Workers
	status["gen_power"] = "max"
	status["gen_workers"] = power.Workers
	status["gen_acquire_limit"] = power.AcquireLimit
	status["gen_batch_interval"] = 0
	status["gen_enqueue_max"] = power.EnqueueMax
	status["active_workers"] = atomic.LoadInt32(&thumbActiveWorkers)
	status["active_tasks"] = thumbActiveTasksSnapshot()
	status["metrics"] = thumbMetricsSnapshot()

	blockedAny := false
	for _, mount := range currentMountPaths() {
		if driver115pkg.IsStorageBlocked(mount) {
			blockedAny = true
			break
		}
	}
	status["blocked"] = blockedAny

	indexed := readThumbIndex()
	cacheByDir := map[string]int{}
	for _, path := range indexed {
		dir := stdpath.Dir(path)
		if dir != "" && dir != "." {
			cacheByDir[dir]++
		}
	}
	cachedDirs := make([]gin.H, 0, len(cacheByDir))
	for dir, count := range cacheByDir {
		cachedDirs = append(cachedDirs, gin.H{"dir": dir, "count": count})
	}
	sort.Slice(cachedDirs, func(i, j int) bool { return cachedDirs[i]["count"].(int) > cachedDirs[j]["count"].(int) })
	status["cached_by_dir"] = cachedDirs

	byDir := map[string]int{}
	unknown := 0
	for _, failure := range fails {
		if failure.Dir == "" {
			unknown++
			continue
		}
		byDir[failure.Dir]++
	}
	dirs := make([]gin.H, 0, len(byDir))
	for dir, count := range byDir {
		dirs = append(dirs, gin.H{"dir": dir, "count": count})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["count"].(int) > dirs[j]["count"].(int) })
	status["fails_by_dir"] = dirs
	status["fails_unknown"] = unknown
	failItems := make([]gin.H, 0, len(fails))
	for _, failure := range fails {
		if failure.Path != "" {
			failItems = append(failItems, gin.H{"path": failure.Path, "dir": failure.Dir, "msg": failure.Msg, "at": failure.At})
		}
	}
	status["fail_items"] = failItems
	status["stale_by_dir"] = thumbStaleByDir(indexed)
	status["mounts"] = currentMountPaths()
	common.SuccessResp(c, status)
}

func ThumbQueuePause(c *gin.Context) {
	thumbQueuePaused.Store(true)
	prewarmStart().Pause()
	cancelActiveGeneration()
	common.SuccessResp(c, gin.H{"paused": true})
}

func ThumbQueueResume(c *gin.Context) {
	thumbQueuePaused.Store(false)
	manager := prewarmStart()
	manager.SetWorkersNumActive(int64(thumbGenPower().Workers))
	manager.Start()
	common.SuccessResp(c, gin.H{"paused": false})
}

func ThumbQueueClear(c *gin.Context) {
	cancelActiveGeneration()
	dropped := thumbPrewarmReset(!thumbQueuePaused.Load())
	common.SuccessResp(c, gin.H{"dropped": dropped})
}
