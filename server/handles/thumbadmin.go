package handles

import (
	"bytes"
	"context"
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
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
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
	apiURL := common.GetApiUrl(c)
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
		for _, obj := range objs {
			if obj.IsDir() {
				if req.Recursive {
					scanDir(dir + "/" + obj.GetName())
				}
				continue
			}
			if utils.GetFileType(obj.GetName()) != conf.VIDEO {
				continue
			}
			rawPath := dir + "/" + obj.GetName()
			if excluded[rawPath] {
				continue
			}
			if req.Force {
				if err := os.Remove(thumbCachePath(thumbKindVideo, rawPath)); err == nil {
					removed++
				}
			}
			prewarmEnqueue(thumbKindVideo, rawPath, apiURL)
			queued++
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

// ThumbStatus GET /api/admin/thumb/status
// 缩略图缓存与预热队列状态（含按目录失败统计）
func ThumbStatus(c *gin.Context) {
	cached, failCount, totalSize := thumbCacheStats()
	status := gin.H{
		"cache_dir":       thumbDir(),
		"cached_files":    cached,
		"fail_markers":    failCount,
		"cache_size":      totalSize,
		"prewarm_enabled": setting.GetStr(conf.ThumbPrewarm, "true") == "true",
	}
	if prewarmCh != nil {
		status["prewarm_queued"] = len(prewarmCh)
	}
	pw := thumbGenPower()
	status["worker_concurrency"] = pw.Workers
	status["gen_power"] = "max"
	status["gen_workers"] = pw.Workers
	status["gen_acquire_limit"] = pw.AcquireLimit
	status["gen_batch_interval"] = 0
	status["gen_enqueue_max"] = pw.EnqueueMax
	status["active_workers"] = atomic.LoadInt32(&thumbActiveWorkers)
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

	// 失效挂载路径目录：索引中不属于任何当前存储挂载路径的条目（挂载路径变更后遗留）
	status["stale_by_dir"] = thumbStaleByDir(indexed)
	status["mounts"] = currentMountPaths()
	common.SuccessResp(c, status)
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
	apiURL := common.GetApiUrl(c)
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
		prewarmEnqueue(f.Kind, f.Path, apiURL)
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
	// 索引：每目录已有缩略图数（直接子项）
	indexed := readThumbIndex()
	cachedByDir := map[string]int{}
	for _, p := range indexed {
		dir := stdpath.Dir(p)
		if dir != "" && dir != "." {
			cachedByDir[dir]++
		}
	}
	root := &thumbTreeNode{}
	dirsCount := 0
	scanFailed := 0
	var scan func(dir string, cur *thumbTreeNode)
	scan = func(dir string, cur *thumbTreeNode) {
		if scanCtx.Err() != nil {
			return
		}
		if dirsCount >= thumbScanMaxDirs {
			return
		}
		dirsCount++
		objs, err := fs.List(scanCtx, dir, &fs.ListArgs{})
		if err != nil {
			scanFailed++
			return
		}
		for _, obj := range objs {
			if obj.IsDir() {
				if obj.GetName() == "_thumbnails" {
					continue
				}
				childPath := dir + "/" + obj.GetName()
				child := &thumbTreeNode{Path: childPath, Name: obj.GetName(), Cached: cachedByDir[childPath]}
				cur.Children = append(cur.Children, child)
				scan(childPath, child)
			} else if utils.GetFileType(obj.GetName()) == conf.VIDEO {
				cur.Videos++
			}
		}
	}
	mounts := currentMountPaths()
	if len(mounts) > 0 {
		for _, m := range mounts {
			child := &thumbTreeNode{Path: m, Name: strings.TrimPrefix(m, "/"), Cached: cachedByDir[m]}
			root.Children = append(root.Children, child)
			scan(m, child)
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
				child = &thumbTreeNode{Path: path, Name: part, Cached: cnt}
				cur.Children = append(cur.Children, child)
			}
			cur = child
		}
	}
	status := "complete"
	if scanFailed > 0 || scanCtx.Err() != nil || dirsCount == 0 {
		status = "partial"
	}
	return root.Children, status
}

// ThumbDir GET /api/admin/thumb/dir?path=
// 返回指定目录下（含子目录）已有缩略图的视频文件清单（来自索引，不依赖网盘列表）
func ThumbDir(c *gin.Context) {
	path := strings.TrimSuffix(c.Query("path"), "/")
	indexed := readThumbIndex()
	var files []string
	total := 0
	prefix := path + "/"
	for _, p := range indexed {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		total++
		if len(files) < 200 {
			files = append(files, p)
		}
	}
	ex := readThumbExcluded()
	var exFiles []string
	for _, f := range files {
		if ex[f] {
			exFiles = append(exFiles, f)
		}
	}
	common.SuccessResp(c, gin.H{"path": path, "files": files, "count": total, "excluded": exFiles})
}

// ThumbExcludeReq POST /api/admin/thumb/exclude
type ThumbExcludeReq struct {
	Paths   []string `json:"paths"`
	Exclude bool     `json:"exclude"` // true=排除（不生成缩略图），false=恢复
}

// readThumbExcluded 读取被排除的视频路径集合
func readThumbExcluded() map[string]bool {
	m := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(thumbDir(), "excluded.jsonl"))
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			m[line] = true
		}
	}
	return m
}

func writeThumbExcluded(paths []string) error {
	var sb strings.Builder
	for _, p := range paths {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(thumbDir(), "excluded.jsonl"), []byte(sb.String()), 0o666)
}

// ThumbExclude 排除/恢复指定视频的缩略图生成（持久化到 excluded.jsonl）
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
// 清空全部缩略图缓存与索引（含未索引的孤儿缓存文件），保留排除列表 excluded.jsonl；
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
		if name == "index.jsonl" {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
		thumbDirsMu.Lock()
	thumbDirsCache = map[string]struct {
		at   time.Time
		data []ThumbDirsEntry
	}{}
	thumbDirsMu.Unlock()
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
				newC := thumbCachePath(kind, newPath)
				if _, err := os.Stat(oldC); err == nil {
					if _, err := os.Stat(newC); err != nil {
						if os.Rename(oldC, newC) == nil {
							migrated++
						}
					} else {
						_ = os.Remove(oldC)
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
	for _, file := range []string{"excluded.jsonl"} {
		p := filepath.Join(thumbDir(), file)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var sb strings.Builder
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, oldP) {
				line = newP + strings.TrimPrefix(line, oldP)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		_ = os.WriteFile(p, []byte(sb.String()), 0o666)
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

// writeThumbIndex 重写缩略图索引文件
func writeThumbIndex(paths []string) error {
	var sb strings.Builder
	for _, p := range paths {
		sb.WriteString(`{"path":` + strconv.Quote(p) + `,"at":""}` + "\n")
	}
	return os.WriteFile(thumbIndexPath(), []byte(sb.String()), 0o666)
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
// 上传走 115 驱动客户端（自动使用"上传代理"，与缩略图下载代理分离）；与后台生成互不阻塞。
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
	// 收集该目录下已有本地缩略图的视频
	indexed := readThumbIndex()
	seen := map[string]bool{}
	var targets []string
	for _, p := range indexed {
		if stdpath.Dir(p) != dir || seen[p] {
			continue
		}
		if _, err := os.ReadFile(thumbCachePath(thumbKindVideo, p)); err != nil {
			continue
		}
		seen[p] = true
		targets = append(targets, p)
	}
	if len(targets) == 0 {
		common.SuccessResp(c, gin.H{"uploaded": 0, "failed": 0, "total": 0})
		return
	}
	utils.Log.Infof("thumb upload targets dir=%s n=%d", dir, len(targets))
	// 并行上传（每个文件独立任务，与生成并发共享 115 上传代理）
	const uploadConcurrency = 4
	sem := make(chan struct{}, uploadConcurrency)
	var (
		mu       sync.Mutex
		uploaded int
		failed   int
	)
	var wg sync.WaitGroup
	for _, p := range targets {
		wg.Add(1)
		go func(rawPath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := os.ReadFile(thumbCachePath(thumbKindVideo, rawPath))
			if err != nil {
				utils.Log.Errorf("thumb upload read cache failed rawPath=%s: %+v", rawPath, err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			if err := uploadThumbManual(c.Request.Context(), rawPath, data); err != nil {
				utils.Log.Errorf("thumb upload failed rawPath=%s: %+v", rawPath, err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			mu.Lock()
			uploaded++
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	common.SuccessResp(c, gin.H{"uploaded": uploaded, "failed": failed, "total": len(targets)})
}
