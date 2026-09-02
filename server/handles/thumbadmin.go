package handles

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	stdpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ThumbGenerateReq POST /api/admin/thumb/generate
type ThumbGenerateReq struct {
	Path      string `json:"path" binding:"required"`
	Recursive bool   `json:"recursive"`
	Force     bool   `json:"force"` // 强制重建：先删除已有缓存再入队
}

// ThumbGenerate 手动批量生成指定目录下的视频缩略图
func ThumbGenerate(c *gin.Context) {
	var req ThumbGenerateReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// 115 风控中禁止触发缩略图生成：生成需从网盘下载视频片段（ffmpeg 抽帧），
	// 风控中下载会加剧风控。风控解除后再生成（缩略图上传在生成后立即进行）。
	if blocked, _ := isStorageBlocked(req.Path); blocked {
		common.ErrorStrResp(c, "115 网盘风控中，缩略图需下载视频生成，请稍后再试（风控通常 10-30 分钟）", 429)
		return
	}
	queued := 0
	removed := 0
	failedDirs := 0
	truncated := false
	_ = thumbGenPower() // 生成强度参数由 worker 读取，这里无需引用
	excluded := readThumbExcluded()
	consecListFails := 0
	var scanDir func(dir string)
	scanDir = func(dir string) {
		if consecListFails >= 5 {
			// 列表连续失败（风控迹象）：提前停止扫描，避免无效请求加剧风控
			truncated = true
			return
		}
		objs, err := fs.List(c.Request.Context(), dir, &fs.ListArgs{})
		if err != nil {
			// 目录列表失败（如 115 受限）跳过该目录，连续失败达到阈值则提前停止
			failedDirs++
			consecListFails++
			return
		}
		consecListFails = 0
		// 本目录视频文件（先生成队列时检测本地与网盘）
		var videos []string
		for _, obj := range objs {
			if obj.IsDir() {
				if req.Recursive {
					scanDir(dir + "/" + obj.GetName())
				}
				continue
			}
			if utils.GetFileType(obj.GetName()) == conf.VIDEO {
				rawPath := dir + "/" + obj.GetName()
				thumbRememberObject(thumbKindVideo, rawPath, obj)
				videos = append(videos, rawPath)
			}
		}
		if len(videos) == 0 {
			return
		}
		// 网盘 _thumbnails 清单（1 API/目录带缓存）：网盘已上传的缩略图不再重新生成
		folder := thumbFolderNameForPath(dir)
		remoteNames := loadRemoteThumbListing(c.Request.Context(), dir, folderNameOnly{folder})
		for _, rawPath := range videos {
			if excluded[rawPath] {
				continue
			}
			if req.Force {
				if err := os.Remove(thumbCachePath(thumbKindVideo, rawPath)); err == nil {
					removed++
				}
			} else {
				if _, err := os.Stat(thumbCachePath(thumbKindVideo, rawPath)); err == nil {
					// 已有本地缩略图：跳过，避免重复入队与计数
					continue
				}
				if remoteNames[remoteThumbName(rawPath)] {
					// 网盘已有缩略图（本地已随上传删除）：无需重新生成
					continue
				}
			}
			// 本地与网盘都缺失的视频：清除 done 标记以便重新入队（含之前失败/中断的）
			prewarmDone.Delete(rawPath)
			if prewarmEnqueue(thumbKindVideo, rawPath) {
				queued++
			}
		}
	}
	// 根目录不是有效存储路径：遍历所有挂载
	roots := []string{req.Path}
	if req.Path == "/" {
		if mounts := currentMountPaths(); len(mounts) > 0 {
			roots = mounts
		}
	}
	for _, root := range roots {
		// 逐挂载风控检查：风控中的挂载跳过遍历（不发起 115 列表请求，避免加剧风控）
		if blocked, _ := isStorageBlocked(root); blocked {
			continue
		}
		scanDir(root)
	}
	common.SuccessResp(c, gin.H{
		"queued": queued, "path": req.Path, "recursive": req.Recursive,
		"force": req.Force, "removed": removed,
		"failed_dirs": failedDirs, "truncated": truncated,
	})
}

// 目录树聚合统计缓存：buildThumbTree 完整扫描后填充（覆盖全部目录，含非索引目录的网盘清单），
// ThumbStatus 读取以保持与目录树一致（避免状态接口只统计索引目录导致数字对不上）。
var (
	thumbAggMu         sync.Mutex
	thumbAgg           struct{ cached, local, cloud int }
	thumbAggAt         time.Time
	thumbAggRefreshing atomic.Bool
)

// thumbAggTTL 顶部聚合统计缓存时长：本地磁盘扫描廉价，网盘计数另有 10 分钟缓存，
// 这里用短 TTL 让统计接近实时（前端 10s 轮询即可看到更新）
const thumbAggTTL = 30 * time.Second

// refreshThumbAgg 重算顶部聚合统计（本地磁盘扫描 + 网盘计数缓存，无额外 115 请求）
func refreshThumbAgg(ctx context.Context) {
	lf, _, _ := thumbCacheStats()
	cf, overlap := thumbCloudStats(ctx)
	thumbAggMu.Lock()
	thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = lf+cf-overlap, lf, cf
	thumbAggAt = time.Now()
	thumbAggMu.Unlock()
}

// knownThumbAgg uses only local DB/index state. It is intentionally network-free and gives the
// status endpoint an immediate, stable fallback while the more expensive remote directory scan is
// refreshed in the background.
func knownThumbAgg() (cached, cloud int) {
	union := map[string]struct{}{}
	for _, p := range readThumbIndex() {
		union[p] = struct{}{}
	}
	cloudSet := readThumbCloudIndex()
	for p := range cloudSet {
		union[p] = struct{}{}
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

// ThumbStatus GET /api/admin/thumb/status
// 缩略图缓存与预热队列状态（含按目录失败统计）
func ThumbStatus(c *gin.Context) {
	localCount, failCount, totalSize := thumbCacheStats()
	var cachedFiles, localFiles, cloudFiles int
	thumbAggMu.Lock()
	if time.Since(thumbAggAt) < thumbAggTTL {
		cachedFiles, localFiles, cloudFiles = thumbAgg.cached, thumbAgg.local, thumbAgg.cloud
		thumbAggMu.Unlock()
	} else {
		thumbAggMu.Unlock()
		// Never make the 10s management-page polling path wait for 115 directory enumeration.
		// Return local/DB-known counts immediately and refresh remote truth asynchronously.
		cachedFiles, cloudFiles = knownThumbAgg()
		localFiles = localCount
		thumbAggMu.Lock()
		thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = cachedFiles, localFiles, cloudFiles
		thumbAggAt = time.Now()
		thumbAggMu.Unlock()
		refreshThumbAggAsync()
	}
	status := gin.H{
		"cache_dir":       thumbDir(),
		"cached_files":    cachedFiles,
		"local_files":     localFiles,
		"cloud_files":     cloudFiles,
		"fail_markers":    failCount,
		"cache_size":      totalSize,
		"prewarm_enabled": setting.GetStr(conf.ThumbPrewarm, "true") == "true",
		"queue_paused":    thumbQueuePaused.Load(),
		"auto_upload":     setting.GetStr(conf.ThumbAutoUpload, "false") == "true",
	}
	status["prewarm_queued"] = thumbPrewarmQueued()
	pw := thumbGenPower()
	status["worker_concurrency"] = pw.Workers
	status["gen_power"] = "max"
	status["gen_workers"] = pw.Workers
	status["gen_acquire_limit"] = pw.AcquireLimit
	status["gen_batch_interval"] = 0
	status["gen_enqueue_max"] = pw.EnqueueMax
	status["active_workers"] = atomic.LoadInt32(&thumbActiveWorkers)
	status["active_tasks"] = thumbActiveTasksSnapshot()
	// 是否有任一挂载处于 115 风控（前端提示"生成已暂停"）
	blockedAny := false
	for _, m := range currentMountPaths() {
		if driver115pkg.IsStorageBlocked(m) {
			blockedAny = true
			break
		}
	}
	status["blocked"] = blockedAny
	// 已有缩略图的目录清单（按目录分组，来自路径索引）
	indexed := readThumbIndex()
	cacheByDir := map[string]int{}
	for _, p := range indexed {
		dir := stdpath.Dir(p)
		if dir != "" && dir != "." {
			cacheByDir[dir]++
		}
	}
	cachedDirs := make([]gin.H, 0, len(cacheByDir))
	for dir, cnt := range cacheByDir {
		cachedDirs = append(cachedDirs, gin.H{"dir": dir, "count": cnt})
	}
	sort.Slice(cachedDirs, func(i, j int) bool { return cachedDirs[i]["count"].(int) > cachedDirs[j]["count"].(int) })
	status["cached_by_dir"] = cachedDirs

	// 失败明细（按目录分组）
	fails := listThumbFails()
	byDir := map[string]int{}
	unknown := 0
	for _, f := range fails {
		if f.Dir == "" {
			unknown++
			continue
		}
		byDir[f.Dir]++
	}
	dirs := make([]gin.H, 0, len(byDir))
	for dir, cnt := range byDir {
		dirs = append(dirs, gin.H{"dir": dir, "count": cnt})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["count"].(int) > dirs[j]["count"].(int) })
	status["fails_by_dir"] = dirs
	status["fails_unknown"] = unknown
	// 失败明细（带路径与原因，供前端告警/展示）
	failItems := make([]gin.H, 0, len(fails))
	for _, f := range fails {
		if f.Path == "" {
			continue
		}
		failItems = append(failItems, gin.H{"path": f.Path, "dir": f.Dir, "msg": f.Msg, "at": f.At})
	}
	status["fail_items"] = failItems

	// 失效挂载路径目录：索引中不属于任何当前存储挂载路径的条目（挂载路径变更后遗留）
	status["stale_by_dir"] = thumbStaleByDir(indexed)
	status["mounts"] = currentMountPaths()
	common.SuccessResp(c, status)
}

// ThumbSetAutoReq POST /api/admin/thumb/auto
type ThumbSetAutoReq struct {
	Generate *bool `json:"generate"` // 自动生成缩略图（浏览目录时入队）
	Upload   *bool `json:"upload"`   // 自动上传未上传的本地缩略图
}

// ThumbSetAuto POST /api/admin/thumb/auto
// 用户控制"自动生成缩略图 + 自动上传"，变更记录到存储活动日志
func ThumbSetAuto(c *gin.Context) {
	var req ThumbSetAutoReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	var items []model.SettingItem
	if req.Generate != nil {
		items = append(items, model.SettingItem{
			Key: conf.ThumbPrewarm, Value: strconv.FormatBool(*req.Generate),
			Type: conf.TypeString, Group: model.SINGLE, Flag: model.PUBLIC,
		})
	}
	if req.Upload != nil {
		items = append(items, model.SettingItem{
			Key: conf.ThumbAutoUpload, Value: strconv.FormatBool(*req.Upload),
			Type: conf.TypeString, Group: model.SINGLE, Flag: model.PUBLIC,
		})
	}
	if len(items) == 0 {
		common.ErrorStrResp(c, "empty setting", 400)
		return
	}
	if err := op.SaveSettingItems(items); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	// 记录到存储活动日志（缩略图功能作用于 115 存储）
	msg := ""
	if req.Generate != nil {
		msg += "自动生成缩略图" + map[bool]string{true: "已开启", false: "已关闭"}[*req.Generate]
	}
	if req.Upload != nil {
		if msg != "" {
			msg += "；"
		}
		msg += "自动上传" + map[bool]string{true: "已开启", false: "已关闭"}[*req.Upload]
	}
	for _, m := range currentMountPaths() {
		driver115pkg.RecordActivity(m, driver115pkg.ActivityWarn, "storage_settings", msg)
	}
	// 开启自动上传时启动 worker 并立即扫描一轮
	if req.Upload != nil && *req.Upload {
		StartThumbAuto()
		go autoUploadScanOnce()
	}
	common.SuccessResp(c, gin.H{
		"generate": setting.GetStr(conf.ThumbPrewarm, "true") == "true",
		"upload":   setting.GetStr(conf.ThumbAutoUpload, "false") == "true",
	})
}

// ThumbQueuePause POST /api/admin/thumb/queue/pause
// 暂停缩略图生成队列：worker 停止取任务，已入队任务保留等待恢复。
func ThumbQueuePause(c *gin.Context) {
	thumbQueuePaused.Store(true)
	prewarmStart().Pause()
	// 取消进行中的生成（杀 ffmpeg），让"正在生成"立即停下并与顶部队列状态一致
	cancelActiveGeneration()
	common.SuccessResp(c, gin.H{"paused": true})
}

// ThumbQueueResume POST /api/admin/thumb/queue/resume
// 恢复缩略图生成队列。
func ThumbQueueResume(c *gin.Context) {
	thumbQueuePaused.Store(false)
	manager := prewarmStart()
	manager.SetWorkersNumActive(int64(thumbGenPower().Workers))
	manager.Start()
	common.SuccessResp(c, gin.H{"paused": false})
}

// ThumbQueueClear POST /api/admin/thumb/queue/clear
// 清空当前队列：丢弃所有待处理任务，并清除其去重标记，
// 之后重新点生成可再次入队。返回丢弃的任务数。
func ThumbQueueClear(c *gin.Context) {
	// 先取消进行中的生成，再用新的 tache manager 替换旧队列。
	cancelActiveGeneration()
	dropped := thumbPrewarmReset(!thumbQueuePaused.Load())
	common.SuccessResp(c, gin.H{"dropped": dropped})
}

// currentMountPaths 返回当前所有存储的挂载路径列表
func currentMountPaths() []string {
	storages, _, err := db.GetStorages(1, -1)
	if err != nil {
		return nil
	}
	mounts := make([]string, 0, len(storages))
	for _, s := range storages {
		if s.MountPath != "" {
			mounts = append(mounts, strings.TrimSuffix(s.MountPath, "/"))
		}
	}
	return mounts
}

func anyStorageBlocked() bool {
	for _, mount := range currentMountPaths() {
		if driver115pkg.IsStorageBlocked(mount) {
			return true
		}
	}
	return false
}

// pathBelongsToMounts 判断路径是否属于任一当前挂载路径
func pathBelongsToMounts(p string, mounts []string) bool {
	for _, m := range mounts {
		if p == m || strings.HasPrefix(p, m+"/") {
			return true
		}
	}
	return false
}

// thumbStaleByDir 聚合索引中属于失效挂载路径的目录
func thumbStaleByDir(indexed []string) []gin.H {
	mounts := currentMountPaths()
	byDir := map[string]int{}
	for _, p := range indexed {
		if pathBelongsToMounts(p, mounts) {
			continue
		}
		dir := stdpath.Dir(p)
		if dir != "" && dir != "." {
			byDir[dir]++
		}
	}
	dirs := make([]gin.H, 0, len(byDir))
	for dir, cnt := range byDir {
		dirs = append(dirs, gin.H{"dir": dir, "count": cnt})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["count"].(int) > dirs[j]["count"].(int) })
	return dirs
}

// ThumbRetryFailsReq POST /api/admin/thumb/retry_fails
type ThumbRetryFailsReq struct {
	Path string `json:"path"` // 为空则重试全部失败
}

// ThumbRetryFails 重试失败缩略图（清除失败标记并重新加入预热队列）
func ThumbRetryFails(c *gin.Context) {
	var req ThumbRetryFailsReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	fails := listThumbFails()
	retried := 0
	cleared := 0
	skipped := 0
	for _, f := range fails {
		if req.Path != "" {
			// 指定目录：仅匹配该目录（旧格式无路径时跳过）
			if f.Dir != req.Path {
				continue
			}
		}
		// 风控防呆：跳过处于 115 风控的存储，避免重试加剧风控
		if f.Path != "" {
			if blocked, _ := isStorageBlocked(f.Path); blocked {
				skipped++
				continue
			}
		}
		// 清除失败标记
		failFile := filepath.Join(thumbDir(), f.Kind+"-"+thumbHash(f.Path)+".fail")
		if f.Path == "" {
			// 旧格式（无路径信息）：按文件名匹配删除（kind 从文件名解析，hash 需要原始路径——无法反查）
			// 直接扫描目录删除该 kind 的所有 fail
			entries, _ := os.ReadDir(thumbDir())
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".fail") && strings.HasPrefix(e.Name(), f.Kind+"-") {
					_ = os.Remove(filepath.Join(thumbDir(), e.Name()))
					cleared++
				}
			}
			continue
		}
		_ = os.Remove(failFile)
		cleared++
		prewarmEnqueue(f.Kind, f.Path)
		retried++
	}
	common.SuccessResp(c, gin.H{"retried": retried, "cleared": cleared, "skipped": skipped})
}

func thumbCacheStats() (cached int, failCount int, totalSize int64) {
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".fail") {
			failCount++
			continue
		}
		if strings.HasSuffix(name, ".png") {
			cached++
			if fi, err := e.Info(); err == nil {
				totalSize += fi.Size()
			}
		}
	}
	return
}

// isStorageBlocked 判断路径所属存储是否处于 115 风控状态（返回是否拦截 + 存储名）
func isStorageBlocked(fullPath string) (bool, string) {
	storage, err := fs.GetStorage(fullPath, &fs.GetStoragesArgs{})
	if err != nil {
		return false, ""
	}
	mount := storage.GetStorage().MountPath
	return driver115pkg.IsStorageBlocked(mount), mount
}

// ---------- 目录缩略图完善度 ----------

type ThumbDirsReq struct {
	Path     string `json:"path"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type ThumbDirsEntry struct {
	Dir     string `json:"dir"`
	Videos  int    `json:"videos"`
	Cached  int    `json:"cached"`
	Failed  int    `json:"failed"`
	Missing int    `json:"missing"`
	Status  string `json:"status"`
}

var (
	thumbDirsMu    sync.Mutex
	thumbDirsCache = map[string]struct {
		at   time.Time
		data []ThumbDirsEntry
	}{}
)

const (
	thumbScanMaxDirs   = 4000  // 单次扫描最多遍历目录数（防止过慢）
	thumbScanMaxVideos = 60000 // 单次扫描最多收集视频数
)

func thumbStatusFromCounts(failed, missing int) string {
	if failed > 0 {
		return "failed"
	}
	if missing > 0 {
		return "partial"
	}
	return "complete"
}

// ThumbDirs GET /api/admin/thumb/dirs
// 递归扫描目录树，统计每个目录的视频数/已缓存/失败/缺失，判断缩略图是否完善
func ThumbDirs(c *gin.Context) {
	var req ThumbDirsReq
	_ = c.ShouldBind(&req)
	path := strings.TrimSuffix(req.Path, "/")
	if path == "" {
		path = "/"
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	// 60s 结果缓存
	thumbDirsMu.Lock()
	ent, ok := thumbDirsCache[path]
	thumbDirsMu.Unlock()
	if ok && time.Since(ent.at) < 60*time.Second {
		common.SuccessResp(c, gin.H{
			"items":  sliceThumbDirsPage(ent.data, page, pageSize),
			"total":  len(ent.data),
			"path":   path,
			"cached": true,
		})
		return
	}

	// 当前索引与失败统计（仅统计属于扫描范围的路径）
	indexed := readThumbIndex()
	cachedByDir := map[string]int{}
	for _, p := range indexed {
		if path != "/" && !strings.HasPrefix(p, path+"/") {
			continue
		}
		dir := stdpath.Dir(p)
		if dir != "" && dir != "." {
			cachedByDir[dir]++
		}
	}
	fails := listThumbFails()
	failedByDir := map[string]int{}
	for _, f := range fails {
		if f.Dir == "" {
			continue
		}
		if path != "/" && !strings.HasPrefix(f.Dir, path+"/") {
			continue
		}
		failedByDir[f.Dir]++
	}

	// 递归扫描视频文件
	videoByDir := map[string]int{}
	dirsCount := 0
	videosCount := 0
	truncated := false
	var scan func(dir string) error
	scan = func(dir string) error {
		if dirsCount >= thumbScanMaxDirs || videosCount >= thumbScanMaxVideos {
			truncated = true
			return nil
		}
		dirsCount++
		objs, err := fs.List(c.Request.Context(), dir, &fs.ListArgs{})
		if err != nil {
			return err
		}
		for _, obj := range objs {
			if obj.IsDir() {
				if err := scan(dir + "/" + obj.GetName()); err != nil {
					return err
				}
				continue
			}
			if utils.GetFileType(obj.GetName()) != conf.VIDEO {
				continue
			}
			videoByDir[dir]++
			videosCount++
		}
		return nil
	}
	if err := scan(path); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	// 根目录不是有效存储路径：改为遍历所有挂载目录
	if path == "/" {
		roots := currentMountPaths()
		if len(roots) > 0 {
			for _, root := range roots {
				if err := scan(root); err != nil {
					common.ErrorResp(c, err, 500)
					return
				}
			}
		}
	}

	allDirs := map[string]struct{}{}
	for d := range videoByDir {
		allDirs[d] = struct{}{}
	}
	for d := range cachedByDir {
		allDirs[d] = struct{}{}
	}
	for d := range failedByDir {
		allDirs[d] = struct{}{}
	}
	entries := make([]ThumbDirsEntry, 0, len(allDirs))
	for d := range allDirs {
		videos := videoByDir[d]
		cached := cachedByDir[d]
		failed := failedByDir[d]
		missing := videos - cached
		if missing < 0 {
			missing = 0
		}
		entries = append(entries, ThumbDirsEntry{
			Dir:     d,
			Videos:  videos,
			Cached:  cached,
			Failed:  failed,
			Missing: missing,
			Status:  thumbStatusFromCounts(failed, missing),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Missing != entries[j].Missing {
			return entries[i].Missing > entries[j].Missing
		}
		return entries[i].Dir < entries[j].Dir
	})

	thumbDirsMu.Lock()
	thumbDirsCache[path] = struct {
		at   time.Time
		data []ThumbDirsEntry
	}{time.Now(), entries}
	thumbDirsMu.Unlock()

	common.SuccessResp(c, gin.H{
		"items":     sliceThumbDirsPage(entries, page, pageSize),
		"total":     len(entries),
		"path":      path,
		"truncated": truncated,
		"cached":    false,
	})
}

func sliceThumbDirsPage(items []ThumbDirsEntry, page, pageSize int) []ThumbDirsEntry {
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []ThumbDirsEntry{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// ThumbTree GET /api/admin/thumb/tree
// 扫描完整目录树（含没有缩略图的目录，忽略 _thumbnails），统计每目录视频数 videos
// 与已有缩略图数 cached；扫描失败（115 风控等）时以缩略图索引兜底
// thumbTreeNode 目录树节点
type thumbTreeNode struct {
	Path     string           `json:"path"`
	Name     string           `json:"name"`
	Cached   int              `json:"cached"`
	Local    int              `json:"local"` // 本地缓存存在的缩略图数
	Cloud    int              `json:"cloud"` // 网盘已上传的缩略图数
	Videos   int              `json:"videos"`
	Children []*thumbTreeNode `json:"children"`
}

const (
	thumbTreeScanTO = 30 * time.Second
)

// ThumbTree GET /api/admin/thumb/tree
// 扫描完整目录树（含没有缩略图的目录，忽略 _thumbnails），统计每目录视频数 videos
// 与已有缩略图数 cached；115 风控/超时时以缩略图索引兜底，scan_status 标记是否完整
func ThumbTree(c *gin.Context) {
	children, status := buildThumbTree(c.Request.Context())
	common.SuccessResp(c, gin.H{"children": children, "scan_status": status})
}

func buildThumbTree(ctx context.Context) ([]*thumbTreeNode, string) {
	scanCtx, cancel := context.WithTimeout(ctx, thumbTreeScanTO)
	defer cancel()
	// 索引：每目录已有缩略图数（直接子项），并按存放位置区分本地/网盘
	indexed := readThumbIndex()
	cachedByDir := map[string]int{}
	localByDir := map[string]int{}
	cloudByDir := map[string]int{}
	cloud := readThumbCloudIndex()
	for _, p := range indexed {
		dir := stdpath.Dir(p)
		if dir != "" && dir != "." {
			exists := false
			if _, err := os.Stat(thumbCachePath(thumbKindVideo, p)); err == nil {
				localByDir[dir]++
				exists = true
			}
			if cloud[p] {
				cloudByDir[dir]++
				exists = true
			}
			if exists {
				cachedByDir[dir]++
			}
		}
	}
	root := &thumbTreeNode{}
	dirsCount := 0
	scanFailed := 0
	realDirs := map[string]bool{}
	indexedSet := map[string]bool{}
	for _, p := range indexed {
		indexedSet[p] = true
	}
	var scan func(dir string, cur *thumbTreeNode)
	scan = func(dir string, cur *thumbTreeNode) {
		if scanCtx.Err() != nil {
			return
		}
		if dirsCount >= thumbScanMaxDirs {
			return
		}
		dirsCount++
		realDirs[dir] = true
		objs, err := fs.List(scanCtx, dir, &fs.ListArgs{})
		if err != nil {
			scanFailed++
			return
		}
		// 本目录网盘 _thumbnails 清单（带缓存）：逐视频匹配，本地索引或网盘清单有即算有缩略图
		names := loadRemoteThumbListing(scanCtx, dir, folderNameOnly{thumbFolderNameForPath(dir)})
		for _, obj := range objs {
			if obj.IsDir() {
				if obj.GetName() == "_thumbnails" {
					continue
				}
				childPath := dir + "/" + obj.GetName()
				child := &thumbTreeNode{Path: childPath, Name: obj.GetName()}
				cur.Children = append(cur.Children, child)
				scan(childPath, child)
			} else if utils.GetFileType(obj.GetName()) == conf.VIDEO {
				cur.Videos++
				rawPath := dir + "/" + obj.GetName()
				thumbRememberObject(thumbKindVideo, rawPath, obj)
				inCloud := names[remoteThumbName(rawPath)]
				// 云清单拉取失败（风控/超时返回空）时，回退到数据库中的已上传状态，
				// 避免目录明明有缩略图却显示 0/缺 N
				if len(names) == 0 && !inCloud {
					inCloud = cloud[rawPath]
				}
				localExists := false
				if indexedSet[rawPath] {
					if _, err := os.Stat(thumbCachePath(thumbKindVideo, rawPath)); err == nil {
						localExists = true
					}
				}
				// 仅计数真实存在的缩略图（本地文件或网盘清单命中），
				// 避免删除缩略图后残留的索引条目把 cached 虚高
				if inCloud || localExists {
					cur.Cached++
				}
				if inCloud {
					cur.Cloud++
				}
				if localExists {
					cur.Local++
				}
			}
		}
	}
	mounts := currentMountPaths()
	if len(mounts) > 0 {
		for _, m := range mounts {
			realDirs[m] = true
			child := &thumbTreeNode{Path: m, Name: strings.TrimPrefix(m, "/")}
			root.Children = append(root.Children, child)
			scan(m, child)
		}
	}
	status := "complete"
	if scanFailed > 0 || scanCtx.Err() != nil || dirsCount == 0 {
		status = "partial"
	}
	// 完整扫描后：自动迁移失效的缩略图索引（文件夹移动/挂载根变更时缩略图跟随）
	if status == "complete" {
		if autoMigrateThumbIndex(realDirs) > 0 {
			// 索引已迁移，重建缓存计数并刷新树节点
			indexed = readThumbIndex()
			cachedByDir = map[string]int{}
			localByDir = map[string]int{}
			cloudByDir = map[string]int{}
			cloud = readThumbCloudIndex()
			for _, p := range indexed {
				dir := stdpath.Dir(p)
				if dir != "" && dir != "." {
					cachedByDir[dir]++
					if _, err := os.Stat(thumbCachePath(thumbKindVideo, p)); err == nil {
						localByDir[dir]++
					}
					if cloud[p] {
						cloudByDir[dir]++
					}
				}
			}
			var refreshCached func(ns []*thumbTreeNode)
			refreshCached = func(ns []*thumbTreeNode) {
				for _, n := range ns {
					n.Cached = cachedByDir[n.Path]
					n.Local = localByDir[n.Path]
					n.Cloud = len(loadRemoteThumbListing(scanCtx, n.Path, folderNameOnly{thumbFolderNameForPath(n.Path)}))
					if len(n.Children) > 0 {
						refreshCached(n.Children)
					}
				}
			}
			refreshCached(root.Children)
		}
	}
	// 索引兜底：115 风控导致扫描不到时，把索引目录补入树
	for dir, cnt := range cachedByDir {
		parts := strings.Split(strings.Trim(dir, "/"), "/")
		cur := root
		path := ""
		for _, part := range parts {
			if part == "" {
				continue
			}
			path += "/" + part
			var child *thumbTreeNode
			for _, cnode := range cur.Children {
				if cnode.Path == path {
					child = cnode
					break
				}
			}
			if child == nil {
				child = &thumbTreeNode{Path: path, Name: part, Cached: cnt, Local: localByDir[dir], Cloud: cloudByDir[dir]}
				cur.Children = append(cur.Children, child)
			}
			cur = child
		}
	}
	// 汇总子树计数：Videos/Cached 改为含子目录的累计值，
	// 与"点击生成（递归）"处理的数量一致，避免"缺 N"与实际入队数对不上
	var sumSubtree func(n *thumbTreeNode) (vids, cached, local, cloud int)
	sumSubtree = func(n *thumbTreeNode) (int, int, int, int) {
		v, c, l, cl := n.Videos, n.Cached, n.Local, n.Cloud
		for _, ch := range n.Children {
			cv, cc, cl2, ccl := sumSubtree(ch)
			v += cv
			c += cc
			l += cl2
			cl += ccl
		}
		n.Videos, n.Cached, n.Local, n.Cloud = v, c, l, cl
		return v, c, l, cl
	}
	for _, m := range root.Children {
		sumSubtree(m)
	}
	// 缓存聚合统计（覆盖全部目录，含非索引目录的网盘清单），供 ThumbStatus 保持一致
	tc, tl, tcl := 0, 0, 0
	for _, m := range root.Children {
		tc += m.Cached
		tl += m.Local
		tcl += m.Cloud
	}
	thumbAggMu.Lock()
	thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = tc, tl, tcl
	thumbAggAt = time.Now()
	thumbAggMu.Unlock()
	return root.Children, status
}

// ThumbDir GET /api/admin/thumb/dir?path=
// 返回指定目录下（含子目录）已有缩略图的视频文件清单（来自索引，不依赖网盘列表）
func ThumbDir(c *gin.Context) {
	path := strings.TrimSuffix(c.Query("path"), "/")
	ex := readThumbExcluded()
	// 已生成缩略图的路径集合（仅本目录直接子文件）
	thumbnailed := map[string]bool{}
	for _, p := range readThumbIndex() {
		if stdpath.Dir(p) == path {
			thumbnailed[p] = true
		}
	}
	var files []string
	hasThumb := map[string]bool{}
	failed := map[string]string{}
	for _, f := range listThumbFails() {
		if f.Path != "" && f.Dir == path {
			failed[f.Path] = f.Msg
		}
	}
	var exFiles []string
	withThumb := 0
	// 列出本目录下所有媒体（视频）文件：有缩略图的标记 has_thumb=true，
	// 前端可点击查看；无缩略图的也可展示（生成缺失）。
	objs, err := fs.List(c.Request.Context(), path, &fs.ListArgs{})
	if err != nil {
		// 列表失败（如 115 风控）：回退到索引中该目录的直接子文件（有缩略图的）
		for p := range thumbnailed {
			if len(files) >= 1000 {
				break
			}
			files = append(files, p)
			hasThumb[p] = true
			withThumb++
			if ex[p] {
				exFiles = append(exFiles, p)
			}
		}
		common.SuccessResp(c, gin.H{
			"path": path, "files": files, "count": withThumb,
			"has_thumb": hasThumb, "excluded": exFiles, "failed": failed, "listed": false,
		})
		return
	}
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		if utils.GetFileType(obj.GetName()) != conf.VIDEO {
			continue
		}
		rawPath := path + "/" + obj.GetName()
		thumbRememberObject(thumbKindVideo, rawPath, obj)
		if len(files) >= 1000 {
			break
		}
		files = append(files, rawPath)
		hasThumb[rawPath] = thumbnailed[rawPath]
		if hasThumb[rawPath] {
			withThumb++
		}
		if ex[rawPath] {
			exFiles = append(exFiles, rawPath)
		}
	}
	common.SuccessResp(c, gin.H{
		"path": path, "files": files, "count": withThumb,
		"has_thumb": hasThumb, "excluded": exFiles, "failed": failed, "listed": true,
	})
}

// ThumbExcludeReq POST /api/admin/thumb/exclude
type ThumbExcludeReq struct {
	Paths   []string `json:"paths"`
	Exclude bool     `json:"exclude"` // true=排除（不生成缩略图），false=恢复
}

// readThumbExcluded reads the durable exclusion set from the database.
func readThumbExcluded() map[string]bool {
	m := map[string]bool{}
	if thumbEnsureDBMigration() {
		if paths, err := db.ListExcludedThumbnailPaths(thumbKindVideo); err == nil {
			for _, p := range paths {
				m[p] = true
			}
			return m
		}
	}
	for _, p := range readLegacyThumbLines(filepath.Join(thumbDir(), "excluded.jsonl")) {
		m[p] = true
	}
	return m
}

func writeThumbExcluded(paths []string) error {
	if thumbEnsureDBMigration() {
		records := make([]model.ThumbnailRecord, 0, len(paths))
		for _, p := range paths {
			if p == "" {
				continue
			}
			records = append(records, model.ThumbnailRecord{
				PathKey: thumbPathKey(thumbKindVideo, p),
				Kind:    thumbKindVideo,
				Path:    p,
			})
		}
		return db.ReplaceExcludedThumbnailPaths(thumbKindVideo, records)
	}
	var sb strings.Builder
	for _, p := range paths {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return writeFileAtomic(filepath.Join(thumbDir(), "excluded.jsonl"), []byte(sb.String()), 0o666)
}

// ThumbExclude 排除/恢复指定视频的缩略图生成（持久化到数据库）
func ThumbExclude(c *gin.Context) {
	var req ThumbExcludeReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	cur := readThumbExcluded()
	changed := false
	if req.Exclude {
		for _, p := range req.Paths {
			p = strings.TrimSpace(p)
			if p != "" && !cur[p] {
				cur[p] = true
				changed = true
			}
		}
	} else {
		for _, p := range req.Paths {
			p = strings.TrimSpace(p)
			if cur[p] {
				delete(cur, p)
				changed = true
			}
		}
	}
	if changed {
		var list []string
		for p := range cur {
			list = append(list, p)
		}
		sort.Strings(list)
		if err := writeThumbExcluded(list); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	common.SuccessResp(c, gin.H{"changed": changed, "excluded": len(cur)})
}

// ThumbClearReq POST /api/admin/thumb/clear
type ThumbClearReq struct {
	Path string `json:"path" binding:"required"`
}

// ThumbClear 清空指定目录下所有缩略图（本地缓存 + 索引 + 远程 _thumbnails），立即生效
func ThumbClear(c *gin.Context) {
	var req ThumbClearReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	path := strings.TrimSuffix(req.Path, "/")
	if path == "" {
		common.ErrorStrResp(c, "invalid path", 400)
		return
	}
	// 风控检查：本地缓存与索引清空不依赖网盘，始终执行；
	// 仅当存储处于风控时跳过远程 _thumbnails 删除（remote 模式尽力而为）
	blocked, _ := isStorageBlocked(path)
	ctx := c.Request.Context()
	indexed := readThumbIndex()
	removed := 0
	var keep []string
	prefix := path + "/"
	kinds := []string{thumbKindVideo, thumbKindAudio, thumbKindImage, thumbKindCover}
	for _, p := range indexed {
		if strings.HasPrefix(p, prefix) {
			for _, kind := range kinds {
				_ = os.Remove(thumbCachePath(kind, p))
				_ = os.Remove(thumbFailPath(kind, p))
			}
			removed++
			continue
		}
		keep = append(keep, p)
	}
	// 递归清空远程 _thumbnails（115 remote 模式，风控中跳过）
	remoteSkipped := false
	if !blocked {
		if addition := remoteThumbStore(path); addition != nil {
			folder := addition.ThumbFolderName()
			if folder != "" {
				var clearDir func(dir string)
				clearDir = func(dir string) {
					thumbDir := dir + "/" + folder
					if objs, err := fs.List(ctx, thumbDir, &fs.ListArgs{}); err == nil {
						for _, obj := range objs {
							if !obj.IsDir() {
								_ = fs.Remove(ctx, thumbDir+"/"+obj.GetName())
							}
						}
					}
					if objs, err := fs.List(ctx, dir, &fs.ListArgs{}); err == nil {
						for _, obj := range objs {
							if obj.IsDir() && obj.GetName() != folder {
								clearDir(dir + "/" + obj.GetName())
							}
						}
					}
				}
				clearDir(path)
			}
		}
	} else {
		remoteSkipped = true
	}
	if err := writeThumbIndex(keep); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	thumbDirsMu.Lock()
	thumbDirsCache = map[string]struct {
		at   time.Time
		data []ThumbDirsEntry
	}{}
	thumbDirsMu.Unlock()
	common.SuccessResp(c, gin.H{"removed": removed, "path": path, "remote_skipped": remoteSkipped})
}

// ThumbClearAll POST /api/admin/thumb/clear_all
// 清空全部缩略图缓存与索引（含未索引的孤儿缓存文件），保留数据库中的排除列表；
// 纯本地操作，不依赖网盘，任何情况下都可执行
func ThumbClearAll(c *gin.Context) {
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".fail") {
			_ = os.Remove(filepath.Join(dir, name))
			removed++
			continue
		}
		if name == "index.jsonl" || name == "cloud.jsonl" {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	if thumbEnsureDBMigration() {
		if err := db.ClearThumbnailIndexState(thumbKindVideo); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	thumbCloudMu.Lock()
	thumbCloudSet = nil
	thumbCloudMu.Unlock()
	thumbDirsMu.Lock()
	thumbDirsCache = map[string]struct {
		at   time.Time
		data []ThumbDirsEntry
	}{}
	thumbDirsMu.Unlock()
	common.SuccessResp(c, gin.H{"removed": removed})
}

// ThumbClearFails POST /api/admin/thumb/clear_fails
// 清除全部缩略图失败标记（.fail 文件），仅删除失败记录，不影响已生成缓存
func ThumbClearFails(c *gin.Context) {
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".fail") {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
		removed++
	}
	thumbStatsInvalidate()
	common.SuccessResp(c, gin.H{"removed": removed})
}

// ---------- 挂载路径迁移 ----------

type ThumbMigrateReq struct {
	OldPrefix string `json:"old_prefix" binding:"required"`
	NewPrefix string `json:"new_prefix" binding:"required"`
}

// ThumbMigrate POST /api/admin/thumb/migrate
// 存储挂载路径变更后，将缩略图索引与缓存文件从旧挂载前缀迁移到新前缀
func ThumbMigrate(c *gin.Context) {
	var req ThumbMigrateReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	oldP := strings.TrimSuffix(req.OldPrefix, "/")
	newP := strings.TrimSuffix(req.NewPrefix, "/")
	if oldP == "" || newP == "" || oldP == newP {
		common.ErrorStrResp(c, "invalid prefix", 400)
		return
	}

	paths := readThumbIndex()
	migrated := 0
	seen := map[string]struct{}{}
	var newLines []string
	kinds := []string{thumbKindVideo, thumbKindAudio, thumbKindImage, thumbKindCover}
	for _, p := range paths {
		newPath := p
		if strings.HasPrefix(p, oldP) {
			rel := strings.TrimPrefix(p, oldP)
			newPath = newP + rel
			for _, kind := range kinds {
				oldC := thumbCachePath(kind, p)
				thumbMoveRecord(kind, p, newPath)
				newC := thumbCachePath(kind, newPath)
				if oldC != newC {
					if _, err := os.Stat(oldC); err == nil {
						if _, err := os.Stat(newC); err != nil {
							if os.Rename(oldC, newC) == nil {
								migrated++
							}
						} else {
							_ = os.Remove(oldC)
						}
					}
				}
				oldF := thumbFailPath(kind, p)
				newF := thumbFailPath(kind, newPath)
				if _, err := os.Stat(oldF); err == nil {
					if _, err := os.Stat(newF); err != nil {
						_ = os.Rename(oldF, newF)
					} else {
						_ = os.Remove(oldF)
					}
				}
			}
		}
		if _, ok := seen[newPath]; !ok {
			seen[newPath] = struct{}{}
			newLines = append(newLines, newPath)
		}
	}
	if err := writeThumbIndex(newLines); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	// 同步迁移排除列表中的路径前缀
	excluded := readThumbExcluded()
	if len(excluded) > 0 {
		migratedExcluded := make([]string, 0, len(excluded))
		for p := range excluded {
			if strings.HasPrefix(p, oldP) {
				p = newP + strings.TrimPrefix(p, oldP)
			}
			migratedExcluded = append(migratedExcluded, p)
		}
		sort.Strings(migratedExcluded)
		_ = writeThumbExcluded(migratedExcluded)
	}
	// 清空目录扫描缓存
	thumbDirsMu.Lock()
	thumbDirsCache = map[string]struct {
		at   time.Time
		data []ThumbDirsEntry
	}{}
	thumbDirsMu.Unlock()
	common.SuccessResp(c, gin.H{"migrated": migrated, "indexed": len(newLines)})
}

// writeThumbIndex replaces the durable video-thumbnail index in the database.
// The JSONL writer remains only as a fallback when DB initialization is unavailable.
func writeThumbIndex(paths []string) error {
	if thumbEnsureDBMigration() {
		records := make([]model.ThumbnailRecord, 0, len(paths))
		for _, p := range paths {
			if p == "" {
				continue
			}
			record := model.ThumbnailRecord{
				PathKey:  thumbPathKey(thumbKindVideo, p),
				Kind:     thumbKindVideo,
				Path:     p,
				CacheKey: thumbHash(p),
			}
			if current, err := db.GetThumbnailRecord(record.PathKey); err == nil {
				record.CacheKey = current.CacheKey
				record.RemoteName = current.RemoteName
				record.Fingerprint = current.Fingerprint
				record.ObjectID = current.ObjectID
				record.Size = current.Size
				record.Modified = current.Modified
				record.Cloud = current.Cloud
			}
			records = append(records, record)
		}
		return db.ReplaceIndexedThumbnailPaths(thumbKindVideo, records)
	}
	thumbIndexMu.Lock()
	defer thumbIndexMu.Unlock()
	var sb strings.Builder
	for _, p := range paths {
		sb.WriteString(`{"path":` + strconv.Quote(p) + `,"at":""}` + "\n")
	}
	return writeFileAtomic(thumbIndexPath(), []byte(sb.String()), 0o666)
}

// thumbIndexMigrateMu 保护缩略图索引/缓存文件的迁移类操作（自动迁移与手动迁移）
var thumbIndexMigrateMu sync.Mutex

// autoMigrateThumbIndex 缩略图索引自动跟随目录移动（根治"文件夹移动后缩略图路径失效"）：
// 若索引里的路径目录不在当前真实目录结构（文件夹被移动/挂载根变更），
// 按"目录名唯一匹配"找到新位置，把缓存文件与失败标记 rename 到新路径并重写索引。
// 无法唯一匹配（如重名或目录被删除）则跳过，交由手动"挂载迁移"处理。
// realDirs 为扫描得到的真实目录集合。返回迁移的条目数。
func autoMigrateThumbIndex(realDirs map[string]bool) int {
	if len(realDirs) == 0 {
		return 0
	}
	thumbIndexMigrateMu.Lock()
	defer thumbIndexMigrateMu.Unlock()
	paths := readThumbIndex()
	if len(paths) == 0 {
		return 0
	}
	// oldDir -> newDir（空串表示不可迁移/已跳过）
	dirMap := map[string]string{}
	migrated := 0
	changed := false
	var newPaths []string
	kinds := []string{thumbKindVideo, thumbKindAudio, thumbKindImage, thumbKindCover}
	for _, p := range paths {
		oldDir := stdpath.Dir(p)
		if oldDir == "" || oldDir == "." || realDirs[oldDir] {
			newPaths = append(newPaths, p)
			continue
		}
		newDir, ok := dirMap[oldDir]
		if !ok {
			name := stdpath.Base(oldDir)
			var cands []string
			for d := range realDirs {
				if stdpath.Base(d) == name {
					cands = append(cands, d)
				}
			}
			if len(cands) == 1 {
				newDir = cands[0]
			}
			dirMap[oldDir] = newDir
		}
		if newDir == "" {
			newPaths = append(newPaths, p)
			continue
		}
		newPath := newDir + strings.TrimPrefix(p, oldDir)
		moved := false
		for _, kind := range kinds {
			oldC := thumbCachePath(kind, p)
			thumbMoveRecord(kind, p, newPath)
			newC := thumbCachePath(kind, newPath)
			if oldC != newC {
				if _, err := os.Stat(oldC); err == nil {
					if _, err := os.Stat(newC); err != nil {
						if os.Rename(oldC, newC) == nil {
							migrated++
							moved = true
						}
					} else {
						_ = os.Remove(oldC)
					}
				}
			}
			oldF := thumbFailPath(kind, p)
			newF := thumbFailPath(kind, newPath)
			if _, err := os.Stat(oldF); err == nil {
				if _, err := os.Stat(newF); err != nil {
					_ = os.Rename(oldF, newF)
				} else {
					_ = os.Remove(oldF)
				}
			}
		}
		newPaths = append(newPaths, newPath)
		changed = changed || moved
	}
	if changed {
		_ = writeThumbIndex(newPaths)
		log.Infof("[thumb] auto-migrated %d thumbnail index entries", migrated)
	}
	return migrated
}

// thumbFolderNameForPath 返回指定视频所在存储的缩略图文件夹名（默认 _thumbnails）
func thumbFolderNameForPath(rawPath string) string {
	if storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{}); err == nil {
		if a, ok := storage.GetAddition().(interface{ ThumbFolderName() string }); ok {
			if n := a.ThumbFolderName(); n != "" {
				return n
			}
		}
	}
	return "_thumbnails"
}

// uploadThumbManual 手动将本地缩略图上传到视频所在目录的缩略图文件夹（不经生成并发名额，可与生成并行）
func uploadThumbManual(ctx context.Context, rawPath string, data []byte) error {
	folder := thumbFolderNameForPath(rawPath)
	thumbName := remoteThumbName(rawPath)
	thumbFullPath := stdpath.Dir(rawPath) + "/" + folder + "/" + thumbName
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
	dir := stdpath.Dir(rawPath) + "/" + folder
	return fs.PutDirectly(ctx, dir, s)
}

// ThumbUploadReq POST /api/admin/thumb/upload
type ThumbUploadReq struct {
	Path string `json:"path" binding:"required"`
}

// ThumbUpload POST /api/admin/thumb/upload
// 将指定目录下已生成的本地缩略图并行上传到该目录的缩略图文件夹（_thumbnails）。
// 上传走 115 驱动客户端；如配置静态 proxy_address，则与其它 115 请求使用同一静态出站配置。
func ThumbUpload(c *gin.Context) {
	var req ThumbUploadReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	dir := strings.TrimSuffix(req.Path, "/")
	if dir == "" {
		common.ErrorStrResp(c, "invalid path", 400)
		return
	}
	if blocked, _ := isStorageBlocked(dir); blocked {
		common.ErrorStrResp(c, "115 网盘风控中，暂时无法上传缩略图，请稍后再试", 429)
		return
	}
	targets := collectUploadTargets(dir)
	if len(targets) == 0 {
		common.SuccessResp(c, gin.H{"queued": 0, "total": 0})
		return
	}
	added, total := thumbUploadEnqueue(targets)
	common.SuccessResp(c, gin.H{"queued": added, "total": total})
}

// ThumbUploadAll POST /api/admin/thumb/upload_all
// 一键上传：把所有已有本地缩略图的视频加入上传队列（批量 50 / 间隔 5s / 去重）。
func ThumbUploadAll(c *gin.Context) {
	if anyStorageBlocked() {
		common.ErrorStrResp(c, "115 网盘风控中，上传已暂停，请稍后再试", 429)
		return
	}
	targets := collectUploadTargets("")
	if len(targets) == 0 {
		common.SuccessResp(c, gin.H{"queued": 0, "total": 0})
		return
	}
	added, total := thumbUploadEnqueue(targets)
	common.SuccessResp(c, gin.H{"queued": added, "total": total})
}

// ThumbUploadStatus GET /api/admin/thumb/upload_status
func ThumbUploadStatus(c *gin.Context) {
	tasks := thumbUploadManagerSnapshot()
	queued, running := thumbUploadStateCounts(tasks)
	thumbUploadMu.Lock()
	remaining := thumbUploadTotal - thumbUploadDone - thumbUploadExists - thumbUploadFailed
	if remaining < 0 {
		remaining = 0
	}
	failItems := make([]gin.H, 0, len(thumbUploadFails))
	paths := make([]string, 0, len(thumbUploadFails))
	for p := range thumbUploadFails {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		failItems = append(failItems, gin.H{"path": p, "msg": thumbUploadFails[p]})
	}
	total := thumbUploadTotal
	done := thumbUploadDone
	failed := thumbUploadFailed
	exists := thumbUploadExists
	attempts := thumbUploadAttempts
	thumbUploadMu.Unlock()
	common.SuccessResp(c, gin.H{
		"active":     queued+running > 0,
		"paused":     thumbUploadPaused.Load(),
		"queued":     queued,
		"remaining":  remaining,
		"total":      total,
		"done":       done,
		"failed":     failed,
		"exists":     exists,
		"fails":      len(failItems),
		"attempts":   attempts,
		"fail_items": failItems,
	})
}

// collectUploadTargets 收集有本地缩略图的视频路径（dir 为空表示全部）
func collectUploadTargets(dir string) []string {
	indexed := readThumbIndex()
	seen := map[string]bool{}
	var targets []string
	for _, p := range indexed {
		if dir != "" && stdpath.Dir(p) != dir {
			continue
		}
		if seen[p] {
			continue
		}
		if _, err := os.ReadFile(thumbCachePath(thumbKindVideo, p)); err != nil {
			continue
		}
		seen[p] = true
		targets = append(targets, p)
	}
	return targets
}

// ---- 缩略图上传队列（tache；路径级去重 / 失败自动重试 / 速率窗口）----

const (
	thumbUploadBatchSize   = 50
	thumbUploadInterval    = 5 * time.Second
	thumbUploadPauseCheck  = 5 * time.Second // 暂停/风控中每次检查间隔
	thumbUploadMaxAttempts = 3               // 单个文件最多尝试上传次数（首次 + 2 次自动重试），超次进入失败清单
)

var (
	thumbUploadManagerMu sync.Mutex
	thumbUploadManager   *tache.Manager[*thumbUploadTask]
	thumbUploadEnqueueMu sync.Mutex

	thumbUploadMu       sync.Mutex
	thumbUploadTotal    int
	thumbUploadDone     int
	thumbUploadFailed   int
	thumbUploadExists   int
	thumbUploadAttempts int
	thumbUploadFails    = map[string]string{}
	thumbUploadSeen     = map[string]bool{}
	thumbUploadPaused   atomic.Bool
	thumbUploadEpoch    atomic.Int64

	thumbUploadThrottleMu    sync.Mutex
	thumbUploadThrottleStart time.Time
	thumbUploadThrottleCount int
)

type thumbUploadTask struct {
	tache.Base
	Path      string
	Result    string
	FailMsg   string
	retryable bool
	epoch     int64
	manager   *tache.Manager[*thumbUploadTask]
}

func (t *thumbUploadTask) Retryable() bool {
	return t.retryable
}

func (t *thumbUploadTask) OnBeforeRetry() {
	timer := time.NewTimer(thumbUploadInterval)
	defer timer.Stop()
	select {
	case <-t.Ctx().Done():
		return
	case <-timer.C:
	}
	for thumbUploadPaused.Load() || anyStorageBlocked() {
		timer := time.NewTimer(thumbUploadPauseCheck)
		select {
		case <-t.Ctx().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func thumbUploadThrottle(ctx context.Context) error {
	for {
		thumbUploadThrottleMu.Lock()
		now := time.Now()
		if thumbUploadThrottleStart.IsZero() || now.Sub(thumbUploadThrottleStart) >= thumbUploadInterval {
			thumbUploadThrottleStart = now
			thumbUploadThrottleCount = 0
		}
		if thumbUploadThrottleCount < thumbUploadBatchSize {
			thumbUploadThrottleCount++
			thumbUploadThrottleMu.Unlock()
			return nil
		}
		wait := thumbUploadInterval - now.Sub(thumbUploadThrottleStart)
		thumbUploadThrottleMu.Unlock()
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (t *thumbUploadTask) Run() error {
	t.Result = ""
	t.FailMsg = ""
	t.retryable = true

	for thumbUploadPaused.Load() || anyStorageBlocked() {
		timer := time.NewTimer(thumbUploadPauseCheck)
		select {
		case <-t.Ctx().Done():
			timer.Stop()
			return t.Ctx().Err()
		case <-timer.C:
		}
	}
	if err := thumbUploadThrottle(t.Ctx()); err != nil {
		return err
	}

	dir := stdpath.Dir(t.Path)
	folder := thumbFolderNameForPath(dir)
	names := loadRemoteThumbListing(t.Ctx(), dir, folderNameOnly{folder})
	name := remoteThumbName(t.Path)
	if names[name] {
		thumbCloudRecord(t.Path)
		t.Result = "exists"
		return nil
	}

	data, err := os.ReadFile(thumbCachePath(thumbKindVideo, t.Path))
	if err != nil {
		t.retryable = false
		t.FailMsg = "本地缩略图文件缺失"
		return fmt.Errorf("%s", t.FailMsg)
	}
	if err := uploadThumbManual(t.Ctx(), t.Path, data); err != nil {
		t.FailMsg = err.Error()
		return err
	}
	thumbListingMarkUploaded(dir, name)
	thumbCloudRecord(t.Path)
	t.Result = "done"
	return nil
}

func thumbUploadTerminalAttempts(t *thumbUploadTask) int {
	retry, _ := t.GetRetry()
	return retry + 1
}

func (t *thumbUploadTask) OnSucceeded() {
	if t.epoch == thumbUploadEpoch.Load() {
		thumbUploadMu.Lock()
		thumbUploadAttempts += thumbUploadTerminalAttempts(t)
		switch t.Result {
		case "exists":
			thumbUploadExists++
		default:
			thumbUploadDone++
		}
		delete(thumbUploadFails, t.Path)
		thumbUploadMu.Unlock()
	}
	if t.manager != nil {
		t.manager.Remove(t.GetID())
	}
}

func (t *thumbUploadTask) OnFailed() {
	if t.epoch == thumbUploadEpoch.Load() {
		thumbUploadMu.Lock()
		thumbUploadAttempts += thumbUploadTerminalAttempts(t)
		thumbUploadFailed++
		msg := t.FailMsg
		if msg == "" && t.GetErr() != nil {
			msg = t.GetErr().Error()
		}
		if msg == "" {
			msg = "上传失败"
		}
		thumbUploadFails[t.Path] = msg
		thumbUploadMu.Unlock()
	}
	if t.manager != nil {
		t.manager.Remove(t.GetID())
	}
}

func newThumbUploadManager(running bool) *tache.Manager[*thumbUploadTask] {
	return tache.NewManager[*thumbUploadTask](
		tache.WithWorks(1),
		tache.WithMaxRetry(thumbUploadMaxAttempts-1),
		tache.WithRunning(running),
		tache.WithLogger(thumbTacheLogger),
	)
}

func thumbUploadManagerGet() *tache.Manager[*thumbUploadTask] {
	thumbUploadManagerMu.Lock()
	defer thumbUploadManagerMu.Unlock()
	if thumbUploadManager == nil {
		thumbUploadManager = newThumbUploadManager(!thumbUploadPaused.Load())
	}
	return thumbUploadManager
}

func thumbUploadManagerSnapshot() []*thumbUploadTask {
	return thumbUploadManagerGet().GetAll()
}

func thumbUploadResetRoundLocked() {
	thumbUploadTotal = 0
	thumbUploadDone = 0
	thumbUploadFailed = 0
	thumbUploadExists = 0
	thumbUploadAttempts = 0
	thumbUploadFails = map[string]string{}
	thumbUploadSeen = map[string]bool{}
}

// thumbUploadEnqueue adds tasks to the current tache upload round. When newRound is true and the
// manager is idle, historical counters are reset; manual retry passes false to preserve the round.
func thumbUploadEnqueueInternal(paths []string, newRound bool) (added, total int) {
	if len(paths) == 0 {
		thumbUploadMu.Lock()
		total = thumbUploadTotal
		thumbUploadMu.Unlock()
		return 0, total
	}
	thumbUploadEnqueueMu.Lock()
	defer thumbUploadEnqueueMu.Unlock()
	manager := thumbUploadManagerGet()
	if newRound && len(manager.GetAll()) == 0 {
		thumbUploadMu.Lock()
		thumbUploadResetRoundLocked()
		thumbUploadMu.Unlock()
	}
	epoch := thumbUploadEpoch.Load()
	for _, p := range paths {
		thumbUploadMu.Lock()
		if thumbUploadSeen[p] {
			thumbUploadMu.Unlock()
			continue
		}
		thumbUploadSeen[p] = true
		thumbUploadTotal++
		thumbUploadMu.Unlock()
		task := &thumbUploadTask{Path: p, retryable: true, epoch: epoch, manager: manager}
		manager.Add(task)
		added++
	}
	thumbUploadMu.Lock()
	total = thumbUploadTotal
	thumbUploadMu.Unlock()
	return added, total
}

func thumbUploadEnqueue(paths []string) (added, total int) {
	return thumbUploadEnqueueInternal(paths, true)
}

func thumbUploadStateCounts(tasks []*thumbUploadTask) (queued, running int) {
	for _, t := range tasks {
		switch t.GetState() {
		case tache.StatePending, tache.StateWaitingRetry, tache.StateBeforeRetry:
			queued++
		case tache.StateRunning, tache.StateErrored, tache.StateFailing, tache.StateCanceling:
			running++
		}
	}
	return queued, running
}

// ThumbUploadRetry POST /api/admin/thumb/upload_retry
// 将上传失败清单重新加入上传队列（失败超过自动重试次数后由用户手动触发）
func ThumbUploadRetry(c *gin.Context) {
	thumbUploadMu.Lock()
	paths := make([]string, 0, len(thumbUploadFails))
	for p := range thumbUploadFails {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	thumbUploadFailed -= len(paths)
	if thumbUploadFailed < 0 {
		thumbUploadFailed = 0
	}
	for _, p := range paths {
		delete(thumbUploadFails, p)
		delete(thumbUploadSeen, p)
	}
	thumbUploadMu.Unlock()
	added, total := thumbUploadEnqueueInternal(paths, false)
	common.SuccessResp(c, gin.H{"retried": added, "total": total})
}

// ThumbUploadPause POST /api/admin/thumb/upload/pause
// 暂停上传队列：worker 停止处理（保留队列），可恢复。
func ThumbUploadPause(c *gin.Context) {
	thumbUploadPaused.Store(true)
	thumbUploadManagerGet().Pause()
	common.SuccessResp(c, gin.H{"paused": true})
}

// ThumbUploadResume POST /api/admin/thumb/upload/resume
// 恢复上传队列。
func ThumbUploadResume(c *gin.Context) {
	thumbUploadPaused.Store(false)
	thumbUploadManagerGet().Start()
	common.SuccessResp(c, gin.H{"paused": false})
}

// ThumbUploadClear POST /api/admin/thumb/upload/clear
// 删除上传队列：清空队列并取消进行中批次（相当于停止上传），重置本轮计数。
func ThumbUploadClear(c *gin.Context) {
	thumbUploadEpoch.Add(1)
	thumbUploadManagerMu.Lock()
	old := thumbUploadManager
	dropped := 0
	if old != nil {
		queued, _ := thumbUploadStateCounts(old.GetAll())
		dropped = queued
		old.Pause()
		old.CancelAll()
	}
	thumbUploadManager = newThumbUploadManager(!thumbUploadPaused.Load())
	thumbUploadManagerMu.Unlock()
	thumbUploadMu.Lock()
	thumbUploadResetRoundLocked()
	thumbUploadMu.Unlock()
	thumbUploadThrottleMu.Lock()
	thumbUploadThrottleStart = time.Time{}
	thumbUploadThrottleCount = 0
	thumbUploadThrottleMu.Unlock()
	common.SuccessResp(c, gin.H{"dropped": dropped})
}

// thumbListingMarkUploaded 在目录缩略图清单缓存中标记已上传（去重）
func thumbListingMarkUploaded(dirPath, name string) {
	thumbListingMu.Lock()
	if e, ok := thumbListing[dirPath]; ok {
		e.names[name] = true
		thumbListing[dirPath] = e
	}
	thumbListingMu.Unlock()
}

// ThumbDeleteFolderReq POST /api/admin/thumb/delete_folder
type ThumbDeleteFolderReq struct {
	Path string `json:"path" binding:"required"`
}

// ThumbDeleteFolder POST /api/admin/thumb/delete_folder
// 删除指定目录的缩略图文件夹（默认 _thumbnails，含远程网盘文件夹），
// 同时清空该目录下本地缩略图缓存、失败标记与索引，便于重新生成。
func ThumbDeleteFolder(c *gin.Context) {
	var req ThumbDeleteFolderReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	dir := strings.TrimSuffix(req.Path, "/")
	if dir == "" {
		common.ErrorStrResp(c, "invalid path", 400)
		return
	}
	ctx := c.Request.Context()
	// 1) 删除远程缩略图文件夹
	folder := thumbFolderNameForPath(dir)
	full := stdpath.Join(dir, folder)
	if _, err := fs.Get(ctx, full, &fs.GetArgs{NoLog: true}); err == nil {
		if err := fs.Remove(ctx, full); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	// 2) 清空该目录下本地缓存、失败标记与索引
	indexed := readThumbIndex()
	removed := 0
	var keep []string
	var removedPaths []string
	prefix := dir + "/"
	kinds := []string{thumbKindVideo, thumbKindAudio, thumbKindImage, thumbKindCover}
	for _, p := range indexed {
		if strings.HasPrefix(p, prefix) {
			for _, kind := range kinds {
				_ = os.Remove(thumbCachePath(kind, p))
				_ = os.Remove(thumbFailPath(kind, p))
			}
			removedPaths = append(removedPaths, p)
			removed++
			continue
		}
		keep = append(keep, p)
	}
	if removed > 0 {
		_ = writeThumbIndex(keep)
	}
	// 3) 使缓存失效并清理，避免旧清单/索引导致树计数、生成判断仍认为缩略图存在
	thumbListingInvalidate(dir)
	thumbCloudRemove(removedPaths)
	thumbDeleteReset(removedPaths)
	thumbStatsInvalidate()
	// 4) 记录到存储活动日志
	if len(removedPaths) > 0 {
		for _, m := range currentMountPaths() {
			driver115pkg.RecordActivity(m, driver115pkg.ActivityWarn, driver115pkg.ActivityThumbDelete,
				fmt.Sprintf("删除 %s 缩略图文件夹（%d 个）", dir, len(removedPaths)))
		}
	}
	common.SuccessResp(c, gin.H{"removed": removed, "folder": full})
}

// ThumbView GET /api/admin/thumb/view?path=
// 返回指定视频的缩略图 PNG（优先读本地缓存；缺失时生成并缓存；静态代理异常出白图时回退直连）。
// 供管理页"点击文件查看缩略图"使用，无需下载签名。
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
	if data, err := os.ReadFile(cachePath); err == nil {
		if !isBlankThumb(data) {
			serve(data)
			return
		}
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
	cachePath = thumbCachePath(thumbKindVideo, path)
	_ = writeFileAtomic(cachePath, png, 0o666)
	thumbRecord(path)
	serve(png)
}

// ThumbCandidatesReq POST /api/admin/thumb/candidates
type ThumbCandidatesReq struct {
	Path    string `json:"path"`
	Refresh bool   `json:"refresh"` // 跳过候选缓存，强制重新取帧
}

// ThumbCandidate 候选缩略图（base64 PNG）
type ThumbCandidate struct {
	Index int    `json:"index"`
	At    string `json:"at"`  // 取帧时间点（秒）
	Png   string `json:"png"` // base64 PNG
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
	thumbCandidateCacheTTL    = 10 * time.Minute
	thumbCandidateCacheMax    = 32
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
		"path":              rawPath,
		"candidates":        candidates,
		"sheet":             sheet,
		"recommended_index": entry.recommendedIndex,
		"cached":            cached,
		"risk_blocked":      entry.riskBlocked,
		"truncated":         entry.truncated,
	}
}

func isThumbCandidateRiskError(err error) bool {
	return isThumbRemoteRiskError(err)
}

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

// ThumbCandidates POST /api/admin/thumb/candidates
// 为一个视频生成多个候选缩略图帧（默认 9 个，跳过空白帧），
// 供用户手动挑选喜欢的画面。走 ffmpeg HTTP Range 远程抽帧（与正常生成一致）。
func ThumbCandidates(c *gin.Context) {
	var req ThumbCandidatesReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	rawPath := req.Path
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	if rawPath == "" {
		common.ErrorStrResp(c, "invalid path", 400)
		return
	}

	// 已生成的候选在短时间内复用，避免用户反复打开弹窗时重复访问 115。
	if !req.Refresh {
		if entry, ok := thumbCandidateCacheGet(rawPath); ok {
			common.SuccessResp(c, thumbCandidateResponse(rawPath, entry, true))
			return
		}
	}
	if blocked, _ := isStorageBlocked(rawPath); blocked {
		common.ErrorStrResp(c, "存储风控中，暂不能生成候选缩略图", 423)
		return
	}

	// 候选生成严格单路，并且不与预热 worker 叠加。TryLock 失败时快速返回，
	// 不让请求排队等待，也不让 115 在短时间内收到更多取帧请求。
	select {
	case thumbCandidateGate <- struct{}{}:
	case <-c.Request.Context().Done():
		return
	default:
		common.ErrorStrResp(c, "已有候选缩略图正在生成，请稍后再试", 429)
		return
	}
	defer func() { <-thumbCandidateGate }()
	thumbCandidateActive.Store(true)
	if !thumbGenerationAdmission.TryLock() {
		thumbCandidateActive.Store(false)
		common.ErrorStrResp(c, "缩略图队列正在生成，请先暂停队列后再试", 409)
		return
	}
	defer func() {
		thumbCandidateActive.Store(false)
		thumbGenerationAdmission.Unlock()
	}()
	if atomic.LoadInt32(&thumbActiveWorkers) > 0 {
		common.ErrorStrResp(c, "缩略图队列正在生成，请先暂停队列后再试", 409)
		return
	}
	if !thumbAcquire(c.Request.Context(), 2*time.Second) {
		common.ErrorStrResp(c, "生成并发已满，请稍后再试", 429)
		return
	}
	defer thumbRelease()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 150*time.Second)
	defer cancel()
	storageMount := thumbCandidate115Mount(rawPath)
	frameGap := thumbCandidateFrameGap
	if storageMount != "" {
		frameGap = thumbCandidate115FrameGap
	}
	link, obj, err := fs.Link(ctx, rawPath, model.LinkArgs{Header: thumbLinkHeader()})
	if err != nil {
		if isThumbCandidateRiskError(err) {
			if storageMount != "" {
				driver115pkg.MarkStorageError(storageMount, err)
			}
			common.ErrorStrResp(c, "115 风控拦截，已暂停候选取帧", 423)
		} else {
			common.ErrorResp(c, err, 500)
		}
		return
	}
	defer link.Close()
	thumbRememberObject(thumbKindVideo, rawPath, obj)
	proxy := thumbProxyForPath(rawPath)
	remoteURL, remoteHeader, remoteProxy, sourceCleanup, err := thumbFFmpegSource(ctx, rawPath, link, obj.GetSize(), proxy)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	defer sourceCleanup()

	var positions []string
	if size := obj.GetSize(); size > thumbProbeMinSize {
		if dur := probeVideoDuration(ctx, rawPath); dur > 0 {
			// 按时长 10%~90% 均匀取 9 帧，避开片头片尾常见黑屏。
			for i := 1; i <= videoContactSheetColumns*videoContactSheetRows; i++ {
				positions = append(positions, fmt.Sprintf("%.1f", dur*float64(i)/10.0))
			}
		}
	}
	if len(positions) == 0 {
		// 未知时长：固定时间点兜底。仍然串行，并在每帧前检查风控状态。
		positions = []string{"3", "10", "30", "60", "120", "300", "600", "1800", "3600"}
	}

	framesAtPosition := make([][]byte, len(positions))
	frames := make([]thumbCandidateFrame, 0, len(positions))
	recommendedIndex := 0
	bestScore := 0.0
	riskBlocked := false
	truncated := false
	for i, ss := range positions {
		if i > 0 {
			timer := time.NewTimer(frameGap)
			select {
			case <-ctx.Done():
				timer.Stop()
				truncated = true
				break
			case <-timer.C:
			}
			if truncated {
				break
			}
		}
		if ctx.Err() != nil {
			truncated = true
			break
		}
		if blocked, _ := isStorageBlocked(rawPath); blocked {
			riskBlocked = true
			truncated = true
			break
		}
		data, err := extractVideoFrameAt(ctx, remoteURL, remoteHeader, remoteProxy, ss)
		if err != nil {
			if storageMount == "" {
				continue
			}
			truncated = true
			if isThumbCandidateRiskError(err) {
				riskBlocked = true
				driver115pkg.MarkStorageError(storageMount, err)
			}
			break
		}
		png, err := encodeThumb(data)
		if err != nil || isBlankThumb(png) {
			continue
		}
		framesAtPosition[i] = png
		score := scoreVideoThumb(png)
		frames = append(frames, thumbCandidateFrame{index: i + 1, at: ss, png: png})
		if recommendedIndex == 0 || score > bestScore {
			recommendedIndex = i + 1
			bestScore = score
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
		at:               time.Now(),
		frames:           frames,
		sheet:            sheet,
		recommendedIndex: recommendedIndex,
		riskBlocked:      riskBlocked,
		truncated:        truncated,
	}
	thumbCandidateCacheSet(rawPath, entry)
	common.SuccessResp(c, thumbCandidateResponse(rawPath, entry, false))
}

// ThumbApplyCandidateReq POST /api/admin/thumb/apply_candidate
type ThumbApplyCandidateReq struct {
	Path string `json:"path"`
	Png  string `json:"png"` // base64 PNG
}

// ThumbApplyCandidate POST /api/admin/thumb/apply_candidate
// 将选中的候选缩略图设为该视频的缩略图：覆盖本地缓存与索引，
// remote 模式同步替换网盘同名文件（先删后传），并失效相关缓存。
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
	cachePath := thumbCachePath(thumbKindVideo, rawPath)
	if err := writeFileAtomic(cachePath, png, 0o666); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	thumbRecord(rawPath)
	_ = os.Remove(thumbFailPath(thumbKindVideo, rawPath))
	// 上传/替换网盘同名缩略图（与 thumb_store 模式无关，用户可能手动上传到网盘）
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

// ThumbDeletePathsReq POST /api/admin/thumb/delete_paths
type ThumbDeletePathsReq struct {
	Paths []string `json:"paths"`
}

// ThumbDeletePaths POST /api/admin/thumb/delete_paths
// 按精确路径列表删除缩略图：本地缓存 + 失败标记 + 索引（remote 模式同步删网盘 _thumbnails 文件）。
func ThumbDeletePaths(c *gin.Context) {
	var req ThumbDeletePathsReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Paths) == 0 {
		common.SuccessResp(c, gin.H{"removed": 0})
		return
	}
	ctx := c.Request.Context()
	target := map[string]bool{}
	for _, p := range req.Paths {
		if p != "" {
			target[p] = true
		}
	}
	kinds := []string{thumbKindVideo, thumbKindAudio, thumbKindImage, thumbKindCover}
	indexed := readThumbIndex()
	removed := 0
	var keep []string
	for _, p := range indexed {
		if target[p] {
			for _, kind := range kinds {
				_ = os.Remove(thumbCachePath(kind, p))
				_ = os.Remove(thumbFailPath(kind, p))
			}
			removed++
			continue
		}
		keep = append(keep, p)
	}
	if removed > 0 {
		_ = writeThumbIndex(keep)
	}
	// 使相关缓存失效并清理，避免旧清单/索引导致计数与生成判断错误
	dirs := map[string]struct{}{}
	for p := range target {
		if d := stdpath.Dir(p); d != "" && d != "." {
			dirs[d] = struct{}{}
		}
	}
	for d := range dirs {
		thumbListingInvalidate(d)
	}
	thumbCloudRemove(req.Paths)
	thumbDeleteReset(req.Paths)
	thumbStatsInvalidate()
	// 远程 _thumbnails：逐个删除对应文件（与 thumb_store 模式无关，用户可能手动上传到网盘；风控中跳过）
	remoteSkipped := false
	if blocked, _ := isStorageBlocked(req.Paths[0]); !blocked {
		for _, p := range req.Paths {
			removeRemoteThumb(ctx, p)
		}
	} else {
		remoteSkipped = true
	}
	// 删除后强制刷新受影响目录的 _thumbnails 清单（fs 对象缓存 + thumbListing），
	// 否则生成/计数仍读到删除前的过期清单，误判"网盘已有"而不入队
	for d := range dirs {
		thumbListingInvalidate(d)
		_, _ = fs.List(ctx, stdpath.Join(d, thumbFolderNameForPath(d)), &fs.ListArgs{Refresh: true, NoLog: true})
	}
	// 记录到存储活动日志
	if removed > 0 {
		for _, m := range currentMountPaths() {
			driver115pkg.RecordActivity(m, driver115pkg.ActivityWarn, driver115pkg.ActivityThumbDelete,
				fmt.Sprintf("删除缩略图 %d 个", removed))
		}
	}
	common.SuccessResp(c, gin.H{"removed": removed, "remote_skipped": remoteSkipped})
}
