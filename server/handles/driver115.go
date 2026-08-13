package handles

import (
	"strconv"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
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
	common.SuccessResp(c, gin.H{"cookie": cookie})
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
