package handles

import (
	"strconv"
	"sync"
	"time"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
	driver115sharepkg "github.com/OpenListTeam/OpenList/v4/drivers/115_share"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// Driver115QRCode GET /api/115/qrcode
// 获取 115 登录二维码（base64 PNG）及轮询所需的 uid/time/sign
func Driver115QRCode(c *gin.Context) {
	info, err := driver115pkg.GetQRCode()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, info)
}

// Driver115QRCodeStatus GET /api/115/qrcode/status?uid=&time=&sign=
// 轮询二维码状态: 0 等待, 1 已扫描, 2 已登录, -1 过期, -2 已取消
func Driver115QRCodeStatus(c *gin.Context) {
	uid := c.Query("uid")
	sign := c.Query("sign")
	t, err := strconv.ParseInt(c.Query("time"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "invalid time", 400)
		return
	}
	st, err := driver115pkg.GetQRCodeStatus(uid, t, sign)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, st)
}

type Driver115QRCodeLoginReq struct {
	UID string `json:"uid" binding:"required"`
	App string `json:"app" binding:"required"`
}

// Driver115QRCodeLogin POST /api/115/qrcode/login
// 扫码确认后获取 cookie，app 为绑定的设备（web/android/ios/tv/alipaymini/wechatmini/qandroid）
func Driver115QRCodeLogin(c *gin.Context) {
	var req Driver115QRCodeLoginReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	valid := false
	for _, app := range driver115pkg.LoginApps {
		if app == req.App {
			valid = true
			break
		}
	}
	if !valid {
		common.ErrorStrResp(c, "invalid app", 400)
		return
	}
	cookie, err := driver115pkg.LoginWithQRCode(req.UID, req.App)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	resp := gin.H{"cookie": cookie}
	info, err := driver115pkg.CheckCookie(cookie)
	if err == nil {
		resp["user_id"] = info.UserID
		resp["user_name"] = info.UserName
	}
	common.SuccessResp(c, resp)
}

type Driver115RootFoldersReq struct {
	Cookie string `json:"cookie" binding:"required"`
}

// Driver115RootFolders POST /api/115/root_folders
// 使用 cookie 列出 115 网盘根目录下的文件夹，供选择挂载根
func Driver115RootFolders(c *gin.Context) {
	var req Driver115RootFoldersReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	folders, err := driver115pkg.ListRootFolders(req.Cookie)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{"content": folders})
}

// Driver115ListFoldersReq POST /api/115/list_folders
type Driver115ListFoldersReq struct {
	Cookie string `json:"cookie" binding:"required"`
	FileID string `json:"file_id"`
}

// Driver115ListFolders POST /api/115/list_folders
// 使用 cookie 列出指定文件夹（file_id，空为根目录）下的子文件夹，供逐级浏览选择挂载根
func Driver115ListFolders(c *gin.Context) {
	var req Driver115ListFoldersReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.FileID == "" {
		req.FileID = "0"
	}
	folders, err := driver115pkg.ListFolders(req.Cookie, req.FileID)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{"content": folders, "file_id": req.FileID})
}

// cookie 校验结果内存缓存（5 分钟），减少 115 API 调用
var (
	checkCookieCacheMu sync.Mutex
	checkCookieCache   = map[string]checkCookieEntry{}
)

type checkCookieEntry struct {
	valid bool
	info  *driver115pkg.UserInfo
	msg   string
	at    time.Time
}

const checkCookieCacheTTL = 5 * time.Minute

func checkCookieCached(cookie string) (valid bool, info *driver115pkg.UserInfo, msg string, hit bool) {
	checkCookieCacheMu.Lock()
	defer checkCookieCacheMu.Unlock()
	e, ok := checkCookieCache[cookie]
	if !ok || time.Since(e.at) > checkCookieCacheTTL {
		return false, nil, "", false
	}
	return e.valid, e.info, e.msg, true
}

func checkCookieStore(cookie string, valid bool, info *driver115pkg.UserInfo, msg string) {
	checkCookieCacheMu.Lock()
	checkCookieCache[cookie] = checkCookieEntry{valid: valid, info: info, msg: msg, at: time.Now()}
	if len(checkCookieCache) > 1000 {
		for k, v := range checkCookieCache {
			if time.Since(v.at) > checkCookieCacheTTL {
				delete(checkCookieCache, k)
			}
		}
	}
	checkCookieCacheMu.Unlock()
}

// Driver115CheckCookie POST /api/115/check_cookie
// 校验 115 cookie 是否有效，返回账号信息
func Driver115CheckCookie(c *gin.Context) {
	var req Driver115RootFoldersReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if valid, info, msg, hit := checkCookieCached(req.Cookie); hit {
		if valid {
			common.SuccessResp(c, gin.H{"valid": true, "user_id": info.UserID, "user_name": info.UserName})
		} else {
			common.SuccessResp(c, gin.H{"valid": false, "msg": msg})
		}
		return
	}
	info, err := driver115pkg.CheckCookie(req.Cookie)
	if err != nil {
		checkCookieStore(req.Cookie, false, nil, err.Error())
		common.SuccessResp(c, gin.H{"valid": false, "msg": err.Error()})
		return
	}
	checkCookieStore(req.Cookie, true, info, "")
	common.SuccessResp(c, gin.H{"valid": true, "user_id": info.UserID, "user_name": info.UserName})
}

// Driver115CheckStorage POST /api/115/check_storage
// 校验已配置的 115 存储 cookie 是否有效（支持 115 Cloud 与 115 Share）
func Driver115CheckStorage(c *gin.Context) {
	var req struct {
		MountPath string `json:"mount_path" binding:"required"`
	}
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	d, err := op.GetStorageByMountPath(req.MountPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	var cookie string
	switch a := d.GetAddition().(type) {
	case *driver115pkg.Addition:
		cookie = a.Cookie
	case *driver115sharepkg.Addition:
		cookie = a.Cookie
	default:
		common.ErrorStrResp(c, "storage is not a 115 driver", 400)
		return
	}
	if cookie == "" {
		common.SuccessResp(c, gin.H{"valid": false, "msg": "storage has no cookie configured"})
		return
	}
	info, err := driver115pkg.CheckCookie(cookie)
	if err != nil {
		common.SuccessResp(c, gin.H{"valid": false, "msg": err.Error()})
		return
	}
	common.SuccessResp(c, gin.H{"valid": true, "user_id": info.UserID, "user_name": info.UserName})
}

// Driver115StorageHealth GET /api/115/storage_health
// 列出所有 115 存储的健康状态（cookie 失效提示）
func Driver115StorageHealth(c *gin.Context) {
	storages, err := db.GetEnabledStorages()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	var result []gin.H
	for _, st := range storages {
		if st.Driver != "115 Cloud" && st.Driver != "115 Share" {
			continue
		}
		entry, ok := driver115pkg.GetStorageHealth(st.MountPath)
		item := gin.H{"mount_path": st.MountPath, "driver": st.Driver, "has_error": ok}
		if ok {
			item["invalid"] = entry.Invalid
			item["msg"] = entry.Msg
			item["at"] = entry.At
		}
		result = append(result, item)
	}
	common.SuccessResp(c, gin.H{"content": result})
}
