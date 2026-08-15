package op

import "sync"

var (
	thumbUploadProxyMu sync.RWMutex
	thumbUploadProxy   string
)

// SetThumbUploadProxy 记录当前缩略图"上传侧"代理地址（115 驱动客户端 API 与上传使用）。
// 为空表示未配置（走全局 proxy_address 或直连）。
func SetThumbUploadProxy(addr string) {
	thumbUploadProxyMu.Lock()
	thumbUploadProxy = addr
	thumbUploadProxyMu.Unlock()
}

// GetThumbUploadProxy 返回当前缩略图"上传侧"代理地址（为空表示未配置）
func GetThumbUploadProxy() string {
	thumbUploadProxyMu.RLock()
	defer thumbUploadProxyMu.RUnlock()
	return thumbUploadProxy
}
