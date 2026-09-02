package handles

import (
	"bytes"
	"context"
	"fmt"
	"os"
	stdpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
		clearThumbFailure(f.Kind, f.Path)
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
				clearThumbFailure(kind, p)
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
	removed := 0
	for _, failure := range listThumbFails() {
		if failure.Path == "" {
			continue
		}
		clearThumbFailure(failure.Kind, failure.Path)
		removed++
	}
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
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
// 一键上传：全部本地缩略图都可入队；具体 storage 的风控/速率在 task 执行阶段分别控制，
// 不再因为一个 115 挂载受限就阻塞 OneDrive/Local 等其它存储。
func ThumbUploadAll(c *gin.Context) {
	targets := collectUploadTargets("")
	if len(targets) == 0 {
		common.SuccessResp(c, gin.H{"queued": 0, "total": 0})
		return
	}
	added, total := thumbUploadEnqueue(targets)
	common.SuccessResp(c, gin.H{"queued": added, "total": total})
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
				clearThumbFailure(kind, p)
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
				clearThumbFailure(kind, p)
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
