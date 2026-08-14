package _115

import (
	"encoding/json"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// StringList 兼容 JSON 数组与逗号分隔字符串的字段类型
type StringList []string

func (s *StringList) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*s = arr
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = strings.Split(str, ",")
		return nil
	}
	return nil
}

type Addition struct {
	Cookie        string     `json:"cookie" type:"text" help:"one of QR code token and cookie required"`
	QRCodeToken   string     `json:"qrcode_token" type:"text" help:"one of QR code token and cookie required"`
	QRCodeSource  string     `json:"qrcode_source" type:"select" options:"web,android,ios,tv,alipaymini,wechatmini,qandroid" default:"web" help:"select the QR code device, default web"`
	PageSize      int64      `json:"page_size" type:"number" default:"1000" help:"list api per page size of 115 driver"`
	LimitRate     float64    `json:"limit_rate" type:"float" default:"2" help:"limit all api request rate ([limit]r/1s)"`
	ThumbStore    string     `json:"thumb_store" type:"select" options:"local,remote" default:"local" help:"local: cache thumbnails on server disk; remote: upload thumbnails to a folder next to the videos"`
	ThumbFolder   string     `json:"thumb_folder" type:"text" default:"_thumbnails" help:"folder name to store thumbnails when thumb_store is remote"`
	RootFolderIDs StringList `json:"root_folder_ids" type:"text" help:"mount multiple root folders, comma separated file ids; when empty use root_folder_id"`
	driver.RootID
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

// rootIDs 解析挂载根：优先 root_folder_ids，其次 root_folder_id 逗号分隔，默认整盘 0
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
	for _, v := range a.RootFolderIDs {
		for _, part := range strings.Split(v, ",") {
			add(part)
		}
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
