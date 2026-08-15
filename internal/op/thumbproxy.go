package op

import "sync"

var (
	thumbUploadProxyMu sync.RWMutex
	thumbUploadProxy   string

	globalProxyMu sync.RWMutex
	globalProxy   string
)

// SetThumbUploadProxy 记录当前缩略图"上传侧"代理地址（缩略图模块使用）。
// 为空表示未配置（走全局代理或直连）。
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

// SetGlobalProxy 记录当前"全局出站代理"地址（115 驱动 API/上传等访问侧使用）。
// 为空表示直连。
func SetGlobalProxy(addr string) {
	globalProxyMu.Lock()
	globalProxy = addr
	globalProxyMu.Unlock()
}

// GetGlobalProxy 返回当前"全局出站代理"地址（为空表示直连）
func GetGlobalProxy() string {
	globalProxyMu.RLock()
	defer globalProxyMu.RUnlock()
	return globalProxy
}
