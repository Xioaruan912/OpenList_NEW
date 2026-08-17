package _115

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// 存储活动日志：内存环形缓冲（重启清空），按 mount 键记录上传成功/失败、
// 风控标记等关键事件，供 @manage 活动日志页展示。记录上限：每挂载
// activityCap 条，超出丢弃最旧。
const activityCap = 100

type ActivityLevel string

const (
	ActivitySuccess ActivityLevel = "success"
	ActivityError   ActivityLevel = "error"
	ActivityWarn    ActivityLevel = "warn"
)

type ActivityAction string

const (
	ActivityUploadSuccess ActivityAction = "upload_success" // 分片上传成功
	ActivitySecUpload     ActivityAction = "sec_upload"     // 秒传成功
	ActivityUploadFail    ActivityAction = "upload_fail"    // 上传失败
	ActivityStorageError  ActivityAction = "storage_error"  // 存储操作错误
	ActivityBlocked       ActivityAction = "blocked"        // 风控标记
	ActivityUnblocked     ActivityAction = "unblocked"      // 风控解除
	ActivitySettings      ActivityAction = "settings"       // 功能开关变更
)

type ActivityEntry struct {
	At     time.Time      `json:"at"`
	Level  ActivityLevel  `json:"level"`
	Action ActivityAction `json:"action"`
	Mount  string         `json:"mount"`
	Path   string         `json:"path,omitempty"`
	Msg    string         `json:"msg"`
}

var (
	activityMu sync.Mutex
	activity   = map[string][]ActivityEntry{} // mount -> 最近事件（新->旧）
)

// RecordActivity 记录一条存储活动日志
func RecordActivity(mount string, level ActivityLevel, action ActivityAction, msg string) {
	RecordActivityWithPath(mount, level, action, "", msg)
}

// RecordActivityWithPath 记录一条存储活动日志（带文件/目录路径）
func RecordActivityWithPath(mount string, level ActivityLevel, action ActivityAction, path, msg string) {
	if mount == "" {
		return
	}
	mount = strings.TrimSuffix(mount, "/")
	activityMu.Lock()
	defer activityMu.Unlock()
	list := activity[mount]
	entry := ActivityEntry{
		At:     time.Now(),
		Level:  level,
		Action: action,
		Mount:  mount,
		Path:   path,
		Msg:    msg,
	}
	list = append(list, entry)
	if len(list) > activityCap {
		list = list[len(list)-activityCap:]
	}
	activity[mount] = list
}

// GetActivityLogs 读取活动日志；mount 为空时聚合所有挂载。
// limit<=0 或大于总条数时返回全部，结果按时间倒序（新->旧）。
func GetActivityLogs(mount string, limit int) []ActivityEntry {
	activityMu.Lock()
	defer activityMu.Unlock()
	var entries = []ActivityEntry{}
	if mount != "" {
		entries = append(entries, activity[strings.TrimSuffix(mount, "/")]...)
	} else {
		for _, list := range activity {
			entries = append(entries, list...)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].At.After(entries[j].At)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// MarkBlockedActivity 风控标记事件（供 health 调用）
func MarkBlockedActivity(mount, msg string) {
	RecordActivity(mount, ActivityWarn, ActivityBlocked, msg)
}

// MarkUnblockedActivity 风控解除事件（供 health 调用）
func MarkUnblockedActivity(mount string) {
	RecordActivity(mount, ActivitySuccess, ActivityUnblocked, "风控解除，已恢复正常")
}