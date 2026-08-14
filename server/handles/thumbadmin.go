package handles

import (
	"os"
	stdpath "path"
	"path/filepath"
	"sort"
	"strings"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// ThumbGenerateReq POST /api/admin/thumb/generate
type ThumbGenerateReq struct {
	Path      string `json:"path" binding:"required"`
	Recursive bool   `json:"recursive"`
}

// ThumbGenerate 手动批量生成指定目录下的视频缩略图
func ThumbGenerate(c *gin.Context) {
	var req ThumbGenerateReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// 风控防呆：115 风控中禁止触发缩略图生成
	if blocked, _ := isStorageBlocked(req.Path); blocked {
		common.ErrorStrResp(c, "115 网盘正在风控保护中，请稍后再试（通常 30-60 分钟）", 429)
		return
	}
	apiURL := common.GetApiUrl(c)
	queued := 0
	var scanDir func(dir string) error
	scanDir = func(dir string) error {
		objs, err := fs.List(c.Request.Context(), dir, &fs.ListArgs{})
		if err != nil {
			return err
		}
		for _, obj := range objs {
			if obj.IsDir() {
				if req.Recursive {
					if err := scanDir(dir + "/" + obj.GetName()); err != nil {
						return err
					}
				}
				continue
			}
			if utils.GetFileType(obj.GetName()) != conf.VIDEO {
				continue
			}
			prewarmEnqueue(thumbKindVideo, dir+"/"+obj.GetName(), apiURL)
			queued++
		}
		return nil
	}
	if err := scanDir(req.Path); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{"queued": queued, "path": req.Path, "recursive": req.Recursive})
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
	common.SuccessResp(c, status)
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
	for _, f := range fails {
		if req.Path != "" {
			// 指定目录：仅匹配该目录（旧格式无路径时跳过）
			if f.Dir != req.Path {
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
	common.SuccessResp(c, gin.H{"retried": retried, "cleared": cleared})
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
