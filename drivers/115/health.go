package _115

import (
	"errors"
	"strings"
	"sync"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

// 115 存储健康标记：驱动操作失败时记录，前端展示 cookie 失效提示
var (
	healthMu sync.Mutex
	health   = map[string]HealthEntry{}
	blocked  = map[string]time.Time{} // 风控标记（特征错误后 N 分钟内拦截写操作）
)

type HealthEntry struct {
	Invalid bool      `json:"invalid"`
	Msg     string    `json:"msg"`
	At      time.Time `json:"at"`
}

// MarkStorageError 记录 115 驱动操作错误（ErrNotLogin 判定 cookie 失效；
// 风控特征错误（拦截页/服务器开小差/登录超时）同时标记风控状态）
func MarkStorageError(mount string, err error) {
	if err == nil {
		return
	}
	healthMu.Lock()
	defer healthMu.Unlock()
	entry := HealthEntry{
		Invalid: errors.Is(err, driver115.ErrNotLogin),
		Msg:     err.Error(),
		At:      time.Now(),
	}
	health[mount] = entry
	if isRiskControlError(err) {
		blocked[mount] = time.Now()
		RecordActivityWithPath(mount, ActivityWarn, ActivityBlocked, "", "风控标记: "+err.Error())
	} else {
		RecordActivityWithPath(mount, ActivityError, ActivityStorageError, "", err.Error())
	}
	if len(health) > 200 {
		for k, v := range health {
			if time.Since(v.At) > 7*24*time.Hour {
				delete(health, k)
			}
		}
	}
}

// isRiskControlError 判断是否 115 风控类错误（WAF 拦截页 / 软风控提示 / 登录超时）
func isRiskControlError(err error) bool {
	msg := err.Error()
	if errors.Is(err, driver115.ErrNotLogin) {
		return true
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "blocked") ||
		strings.Contains(lower, "服务器开小差") ||
		strings.Contains(lower, "登录超时") ||
		strings.Contains(lower, "user not login") ||
		strings.Contains(lower, "405") ||
		strings.Contains(lower, "forbidden")
}

// IsStorageBlocked 该存储最近 5 分钟内是否处于风控状态（用于拦截写操作）
func IsStorageBlocked(mount string) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	t, ok := blocked[mount]
	if !ok {
		return false
	}
	if time.Since(t) > 5*time.Minute {
		delete(blocked, mount)
		MarkUnblockedActivity(mount)
		return false
	}
	return true
}

// 上传风控连续失败检测：上传被 115 拒绝（如 990005 "非法参数错误"，115 用通用
// 参数错误掩盖上传限流）连续出现时标记存储风控，让上传队列自动降速（20/5s），
// 给账号冷却空间，避免继续满速打加重风控。
var uploadRiskFails = map[string][]time.Time{} // mount -> 最近上传风控失败时间

const (
	uploadRiskWindow    = 2 * time.Minute
	uploadRiskThreshold = 3
)

// isUploadRiskError 判断是否为上传侧风控错误
func isUploadRiskError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "990005") || strings.Contains(msg, "非法参数错误")
}

// MarkUploadError 上传失败时调用：记录健康信息并累计上传风控失败，
// 窗口内达到阈值则标记风控（上传队列降速、目录上传暂缓）。
func MarkUploadError(mount string, err error) {
	if err == nil {
		return
	}
	MarkStorageError(mount, err)
	if !isUploadRiskError(err) {
		return
	}
	now := time.Now()
	healthMu.Lock()
	defer healthMu.Unlock()
	list := uploadRiskFails[mount][:0]
	for _, t := range uploadRiskFails[mount] {
		if now.Sub(t) < uploadRiskWindow {
			list = append(list, t)
		}
	}
	list = append(list, now)
	if len(list) >= uploadRiskThreshold {
		blocked[mount] = now
		MarkBlockedActivity(mount, "上传风控连续失败，已标记风控并降速")
		uploadRiskFails[mount] = nil
	} else {
		uploadRiskFails[mount] = list
	}
}

// GetStorageHealth 读取存储健康状态
func GetStorageHealth(mount string) (HealthEntry, bool) {
	healthMu.Lock()
	defer healthMu.Unlock()
	e, ok := health[mount]
	return e, ok
}
