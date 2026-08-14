package handles

import (
	"os"
	"strings"

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
// 缩略图缓存与预热队列状态
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
	common.SuccessResp(c, status)
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
