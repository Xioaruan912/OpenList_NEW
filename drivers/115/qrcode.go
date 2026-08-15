package _115

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

// LoginApps 可选的扫码登录设备，linux/mac/windows 已被 115 官方下架
var LoginApps = []string{"web", "android", "ios", "tv", "alipaymini", "wechatmini", "qandroid"}

// QRCodeInfo 二维码会话信息，qrcode 为 base64 编码的 PNG 图片
type QRCodeInfo struct {
	UID    string `json:"uid"`
	Time   int64  `json:"time"`
	Sign   string `json:"sign"`
	QRCode string `json:"qrcode"`
}

// QRCodeStatusInfo 二维码扫码状态
type QRCodeStatusInfo struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

// FolderInfo 115 网盘中的文件夹
type FolderInfo struct {
	FileID   string `json:"file_id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

func newQRCodeClient() *driver115.Pan115Client {
	opts := []driver115.Option{
		driver115.UA(getQRCodeUA()),
		func(c *driver115.Pan115Client) {
			c.Client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
		},
	}
	if px := conf.Conf.ProxyAddress; px != "" {
		opts = append(opts, driver115.WithProxy(px))
	}
	return driver115.New(opts...)
}

func getQRCodeUA() string {
	return fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)
}

// GetQRCode 获取登录二维码，无需任何凭据
func GetQRCode() (*QRCodeInfo, error) {
	s, err := newQRCodeClient().QRCodeStart()
	if err != nil {
		return nil, err
	}
	png, err := s.QRCode()
	if err != nil {
		return nil, err
	}
	return &QRCodeInfo{
		UID:    s.UID,
		Time:   s.Time,
		Sign:   s.Sign,
		QRCode: base64.StdEncoding.EncodeToString(png),
	}, nil
}

// GetQRCodeStatus 轮询二维码状态: 0 等待, 1 已扫描, 2 已登录, -1 过期, -2 已取消
func GetQRCodeStatus(uid string, t int64, sign string) (*QRCodeStatusInfo, error) {
	st, err := newQRCodeClient().QRCodeStatus(&driver115.QRCodeSession{
		UID:  uid,
		Time: t,
		Sign: sign,
	})
	if err != nil {
		return nil, err
	}
	return &QRCodeStatusInfo{Status: st.Status, Msg: st.Msg}, nil
}

// LoginWithQRCode 扫码确认后获取登录凭证并生成 cookie
func LoginWithQRCode(uid, app string) (string, error) {
	cr, err := newQRCodeClient().QRCodeLoginWithApp(
		&driver115.QRCodeSession{UID: uid},
		driver115.LoginApp(app),
	)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UID=%s;CID=%s;SEID=%s;KID=%s", cr.UID, cr.CID, cr.SEID, cr.KID), nil
}

// ListRootFolders 使用 cookie 列出网盘根目录下的所有文件夹
func ListRootFolders(cookie string) ([]FolderInfo, error) {
	return ListFolders(cookie, "0")
}

// ListFolders 使用 cookie 列出指定 file_id 目录下的所有文件夹（file_id="0" 为根目录）
func ListFolders(cookie, fileID string) ([]FolderInfo, error) {
	client := newQRCodeClient()
	cr := &driver115.Credential{}
	if err := cr.FromCookie(cookie); err != nil {
		return nil, err
	}
	client.ImportCredential(cr)
	files, err := client.ListWithLimit(fileID, 1000, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	var folders []FolderInfo
	for _, f := range *files {
		if f.IsDirectory {
			folders = append(folders, FolderInfo{
				FileID:   f.FileID,
				ParentID: f.ParentID,
				Name:     f.Name,
			})
		}
	}
	return folders, nil
}

// UserInfo 115 账号信息
type UserInfo struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
}

// CheckCookie 校验 cookie 是否有效，返回账号信息
func CheckCookie(cookie string) (*UserInfo, error) {
	client := newQRCodeClient()
	cr := &driver115.Credential{}
	if err := cr.FromCookie(cookie); err != nil {
		return nil, err
	}
	client.ImportCredential(cr)
	u, err := client.GetUser()
	if err != nil {
		return nil, err
	}
	return &UserInfo{
		UserID:   strconv.FormatInt(u.UserID, 10),
		UserName: u.UserName,
	}, nil
}
