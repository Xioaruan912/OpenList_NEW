package _115

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Cookie      string  `json:"cookie" type:"text" help:"115 网盘 Cookie（扫码登录成功后自动填入，无需手动获取）"`
	PageSize    int64   `json:"page_size" type:"number" default:"1000" help:"115 驱动列表接口每页大小"`
	LimitRate   float64 `json:"limit_rate" type:"float" default:"0" help:"限制所有 115 接口请求速率（[limit] 次/秒）；0 表示不限制"`
	ThumbStore  string  `json:"thumb_store" type:"select" options:"local,remote" default:"local" help:"local：缩略图缓存在服务器磁盘；remote：缩略图上传到视频同级的 _thumbnails 文件夹（不占服务器磁盘）"`
	ThumbFolder string  `json:"thumb_folder" type:"text" default:"_thumbnails" help:"thumb_store 为 remote 时，存储缩略图的文件夹名"`
	driver.RootID
}

// GetProxy 返回 115 驱动客户端（API 与上传）使用的代理：
// 优先取"全局出站代理"（/@manage/proxy 全局代理策略），未配置时回退到全局 proxy_address
func (a *Addition) GetProxy() string {
	if px := op.GetGlobalProxy(); px != "" {
		return px
	}
	return conf.Conf.ProxyAddress
}

// ThumbStoreRemote 缩略图是否存到视频所在网盘目录
func (a *Addition) ThumbStoreRemote() bool {
	return a.ThumbStore == "remote"
}

// ThumbFolderName 远程缩略图文件夹名
func (a *Addition) ThumbFolderName() string {
	if a.ThumbFolder == "" {
		return "_thumbnails"
	}
	return a.ThumbFolder
}

// rootIDs 解析挂载根：root_folder_id 支持逗号分隔多根，默认整盘 0
func (a *Addition) rootIDs() []string {
	var ids []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		ids = append(ids, s)
	}
	if a.RootFolderID != "" {
		for _, part := range strings.Split(a.RootFolderID, ",") {
			add(part)
		}
	}
	if len(ids) == 0 {
		ids = []string{"0"}
	}
	return ids
}

var config = driver.Config{
	Name:          "115 Cloud",
	DefaultRoot:   "0",
	LinkCacheMode: driver.LinkCacheUA,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Pan115{}
	})
}
