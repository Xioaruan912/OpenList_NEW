package _115

import (
	"errors"
	"sync"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

// 115 存储健康标记：驱动操作失败时记录，前端展示 cookie 失效提示
var (
	healthMu sync.Mutex
	health   = map[string]HealthEntry{}
)

type HealthEntry struct {
	Invalid bool      `json:"invalid"`
	Msg     string    `json:"msg"`
	At      time.Time `json:"at"`
}

// MarkStorageError 记录 115 驱动操作错误（ErrNotLogin 判定 cookie 失效）
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
	// 清理 7 天前的记录
	if len(health) > 200 {
		for k, v := range health {
			if time.Since(v.At) > 7*24*time.Hour {
				delete(health, k)
			}
		}
	}
}

// GetStorageHealth 读取存储健康状态
func GetStorageHealth(mount string) (HealthEntry, bool) {
	healthMu.Lock()
	defer healthMu.Unlock()
	e, ok := health[mount]
	return e, ok
}
