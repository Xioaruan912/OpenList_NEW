package handles

import (
	"context"
	"fmt"
	"os"
	stdpath "path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/gin-gonic/gin"
)

// Upload task plane: persistence, retry lifecycle, per-storage admission/rate limiting and queue
// controls live here. HTTP handlers that choose which files to upload remain in thumbadmin.go.

const (
	thumbUploadBatchSize   = 50
	thumbUploadInterval    = 5 * time.Second
	thumbUploadPauseCheck  = 5 * time.Second
	thumbUploadMaxAttempts = 3
)

var (
	thumbUploadManagerMu sync.Mutex
	thumbUploadManager   *tache.Manager[*thumbUploadTask]
	thumbUploadEnqueueMu sync.Mutex

	thumbUploadMu       sync.Mutex
	thumbUploadTotal    int
	thumbUploadDone     int
	thumbUploadFailed   int
	thumbUploadExists   int
	thumbUploadAttempts int
	thumbUploadFails    = map[string]string{}
	thumbUploadSeen     = map[string]bool{}
	thumbUploadPaused   atomic.Bool
	thumbUploadEpoch    atomic.Int64

	thumbUploadStorageStates sync.Map
)

type thumbUploadTask struct {
	tache.Base
	Path      string
	Result    string
	FailMsg   string
	retryable bool
	epoch     int64
	manager   *tache.Manager[*thumbUploadTask]
}

func (t *thumbUploadTask) Retryable() bool { return t.retryable }

func (t *thumbUploadTask) OnBeforeRetry() {
	timer := time.NewTimer(thumbUploadInterval)
	defer timer.Stop()
	select {
	case <-t.Ctx().Done():
		return
	case <-timer.C:
	}
	for thumbUploadPaused.Load() {
		timer := time.NewTimer(thumbUploadPauseCheck)
		select {
		case <-t.Ctx().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type thumbUploadStoragePolicy struct {
	Concurrency int
	Batch       int
	Interval    time.Duration
}

type thumbUploadStorageState struct {
	mu          sync.Mutex
	active      int
	windowStart time.Time
	windowCount int
}

func thumbUploadPolicyForPath(rawPath string) (string, thumbUploadStoragePolicy) {
	policy := thumbUploadStoragePolicy{Concurrency: 2, Batch: thumbUploadBatchSize, Interval: thumbUploadInterval}
	key := "default"
	if storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{}); err == nil && storage.GetStorage() != nil {
		s := storage.GetStorage()
		driverName := strings.ToLower(s.Driver)
		key = fmt.Sprintf("%d:%s", s.ID, driverName)
		switch {
		case strings.HasPrefix(driverName, "115"):
			policy = thumbUploadStoragePolicy{Concurrency: 1, Batch: 10, Interval: 5 * time.Second}
		case strings.Contains(driverName, "onedrive"):
			policy = thumbUploadStoragePolicy{Concurrency: 2, Batch: 40, Interval: 5 * time.Second}
		case driverName == "local":
			policy = thumbUploadStoragePolicy{Concurrency: 4, Batch: 200, Interval: time.Second}
		}
	}
	return key, policy
}

func thumbUploadStorageAcquire(ctx context.Context, rawPath string) (func(), error) {
	key, policy := thumbUploadPolicyForPath(rawPath)
	value, _ := thumbUploadStorageStates.LoadOrStore(key, &thumbUploadStorageState{})
	state := value.(*thumbUploadStorageState)
	for {
		if blocked, _ := isStorageBlocked(rawPath); blocked {
			timer := time.NewTimer(thumbUploadPauseCheck)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		state.mu.Lock()
		now := time.Now()
		if state.windowStart.IsZero() || now.Sub(state.windowStart) >= policy.Interval {
			state.windowStart = now
			state.windowCount = 0
		}
		if state.active < policy.Concurrency && state.windowCount < policy.Batch {
			state.active++
			state.windowCount++
			state.mu.Unlock()
			return func() {
				state.mu.Lock()
				if state.active > 0 {
					state.active--
				}
				state.mu.Unlock()
			}, nil
		}
		wait := 100 * time.Millisecond
		if state.windowCount >= policy.Batch {
			wait = policy.Interval - now.Sub(state.windowStart)
		}
		state.mu.Unlock()
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (t *thumbUploadTask) Run() error {
	t.Result = ""
	t.FailMsg = ""
	t.retryable = true

	for thumbUploadPaused.Load() {
		timer := time.NewTimer(thumbUploadPauseCheck)
		select {
		case <-t.Ctx().Done():
			timer.Stop()
			return t.Ctx().Err()
		case <-timer.C:
		}
	}
	release, err := thumbUploadStorageAcquire(t.Ctx(), t.Path)
	if err != nil {
		return err
	}
	defer release()

	dir := stdpath.Dir(t.Path)
	folder := thumbFolderNameForPath(dir)
	names := loadRemoteThumbListing(t.Ctx(), dir, folderNameOnly{folder})
	name := remoteThumbName(t.Path)
	if names[name] {
		thumbCloudRecord(t.Path)
		t.Result = "exists"
		return nil
	}

	data, err := os.ReadFile(thumbCachePath(thumbKindVideo, t.Path))
	if err != nil {
		t.retryable = false
		t.FailMsg = "本地缩略图文件缺失"
		return fmt.Errorf("%s", t.FailMsg)
	}
	if err := uploadThumbManual(t.Ctx(), t.Path, data); err != nil {
		t.FailMsg = err.Error()
		return err
	}
	thumbListingMarkUploaded(dir, name)
	thumbCloudRecord(t.Path)
	t.Result = "done"
	return nil
}

func thumbUploadTerminalAttempts(t *thumbUploadTask) int {
	retry, _ := t.GetRetry()
	return retry + 1
}

func (t *thumbUploadTask) OnSucceeded() {
	if t.epoch == thumbUploadEpoch.Load() {
		thumbUploadMu.Lock()
		thumbUploadAttempts += thumbUploadTerminalAttempts(t)
		if t.Result == "exists" {
			thumbUploadExists++
		} else {
			thumbUploadDone++
		}
		delete(thumbUploadFails, t.Path)
		thumbUploadMu.Unlock()
	}
	manager := t.manager
	if manager == nil {
		manager = thumbUploadManagerGet()
	}
	manager.Remove(t.GetID())
}

func (t *thumbUploadTask) OnFailed() {
	if t.epoch == thumbUploadEpoch.Load() {
		thumbUploadMu.Lock()
		thumbUploadAttempts += thumbUploadTerminalAttempts(t)
		thumbUploadFailed++
		msg := t.FailMsg
		if msg == "" && t.GetErr() != nil {
			msg = t.GetErr().Error()
		}
		if msg == "" {
			msg = "上传失败"
		}
		thumbUploadFails[t.Path] = msg
		thumbUploadMu.Unlock()
	}
	manager := t.manager
	if manager == nil {
		manager = thumbUploadManagerGet()
	}
	manager.Remove(t.GetID())
}

func newThumbUploadManager(running bool) *tache.Manager[*thumbUploadTask] {
	return tache.NewManager[*thumbUploadTask](
		tache.WithWorks(4),
		tache.WithMaxRetry(thumbUploadMaxAttempts-1),
		tache.WithPersistFunction(db.GetTaskDataFunc("thumb_upload", true), db.UpdateTaskDataFunc("thumb_upload", true)),
		tache.WithPersistDebounce(500*time.Millisecond),
		tache.WithRunning(running),
		tache.WithLogger(thumbTacheLogger),
	)
}

func thumbUploadManagerGet() *tache.Manager[*thumbUploadTask] {
	thumbUploadManagerMu.Lock()
	defer thumbUploadManagerMu.Unlock()
	if thumbUploadManager == nil {
		thumbUploadManager = newThumbUploadManager(!thumbUploadPaused.Load())
		tasks := thumbUploadManager.GetAll()
		if len(tasks) > 0 {
			thumbUploadMu.Lock()
			for _, task := range tasks {
				task.manager = thumbUploadManager
				task.epoch = thumbUploadEpoch.Load()
				task.retryable = true
				if task.Path != "" && !thumbUploadSeen[task.Path] {
					thumbUploadSeen[task.Path] = true
					thumbUploadTotal++
				}
			}
			thumbUploadMu.Unlock()
		}
	}
	return thumbUploadManager
}

func thumbUploadManagerSnapshot() []*thumbUploadTask { return thumbUploadManagerGet().GetAll() }

func thumbUploadResetRoundLocked() {
	thumbUploadTotal = 0
	thumbUploadDone = 0
	thumbUploadFailed = 0
	thumbUploadExists = 0
	thumbUploadAttempts = 0
	thumbUploadFails = map[string]string{}
	thumbUploadSeen = map[string]bool{}
}

func thumbUploadEnqueueInternal(paths []string, newRound bool) (added, total int) {
	if len(paths) == 0 {
		thumbUploadMu.Lock()
		total = thumbUploadTotal
		thumbUploadMu.Unlock()
		return 0, total
	}
	thumbUploadEnqueueMu.Lock()
	defer thumbUploadEnqueueMu.Unlock()
	manager := thumbUploadManagerGet()
	if newRound && len(manager.GetAll()) == 0 {
		thumbUploadMu.Lock()
		thumbUploadResetRoundLocked()
		thumbUploadMu.Unlock()
	}
	epoch := thumbUploadEpoch.Load()
	for _, path := range paths {
		thumbUploadMu.Lock()
		if thumbUploadSeen[path] {
			thumbUploadMu.Unlock()
			continue
		}
		thumbUploadSeen[path] = true
		thumbUploadTotal++
		thumbUploadMu.Unlock()
		manager.Add(&thumbUploadTask{Path: path, retryable: true, epoch: epoch, manager: manager})
		added++
	}
	thumbUploadMu.Lock()
	total = thumbUploadTotal
	thumbUploadMu.Unlock()
	return added, total
}

func thumbUploadEnqueue(paths []string) (added, total int) {
	return thumbUploadEnqueueInternal(paths, true)
}

func thumbUploadStateCounts(tasks []*thumbUploadTask) (queued, running int) {
	for _, task := range tasks {
		switch task.GetState() {
		case tache.StatePending, tache.StateWaitingRetry, tache.StateBeforeRetry:
			queued++
		case tache.StateRunning, tache.StateErrored, tache.StateFailing, tache.StateCanceling:
			running++
		}
	}
	return queued, running
}

func thumbUploadStatusSnapshot() gin.H {
	tasks := thumbUploadManagerSnapshot()
	queued, running := thumbUploadStateCounts(tasks)
	thumbUploadMu.Lock()
	remaining := thumbUploadTotal - thumbUploadDone - thumbUploadExists - thumbUploadFailed
	if remaining < 0 {
		remaining = 0
	}
	paths := make([]string, 0, len(thumbUploadFails))
	for path := range thumbUploadFails {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	failItems := make([]gin.H, 0, len(paths))
	for _, path := range paths {
		failItems = append(failItems, gin.H{"path": path, "msg": thumbUploadFails[path]})
	}
	total, done := thumbUploadTotal, thumbUploadDone
	failed, exists := thumbUploadFailed, thumbUploadExists
	attempts := thumbUploadAttempts
	thumbUploadMu.Unlock()
	return gin.H{
		"active": queued+running > 0, "paused": thumbUploadPaused.Load(), "queued": queued,
		"remaining": remaining, "total": total, "done": done, "failed": failed, "exists": exists,
		"fails": len(failItems), "attempts": attempts, "fail_items": failItems,
	}
}

func ThumbUploadStatus(c *gin.Context) { common.SuccessResp(c, thumbUploadStatusSnapshot()) }

func ThumbUploadRetry(c *gin.Context) {
	thumbUploadMu.Lock()
	paths := make([]string, 0, len(thumbUploadFails))
	for path := range thumbUploadFails {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	thumbUploadFailed -= len(paths)
	if thumbUploadFailed < 0 {
		thumbUploadFailed = 0
	}
	for _, path := range paths {
		delete(thumbUploadFails, path)
		delete(thumbUploadSeen, path)
	}
	thumbUploadMu.Unlock()
	added, total := thumbUploadEnqueueInternal(paths, false)
	common.SuccessResp(c, gin.H{"retried": added, "total": total})
}

func ThumbUploadPause(c *gin.Context) {
	thumbUploadPaused.Store(true)
	thumbUploadManagerGet().Pause()
	common.SuccessResp(c, gin.H{"paused": true})
}

func ThumbUploadResume(c *gin.Context) {
	thumbUploadPaused.Store(false)
	thumbUploadManagerGet().Start()
	common.SuccessResp(c, gin.H{"paused": false})
}

func ThumbUploadClear(c *gin.Context) {
	thumbUploadEpoch.Add(1)
	thumbUploadManagerMu.Lock()
	old := thumbUploadManager
	dropped := 0
	if old != nil {
		queued, _ := thumbUploadStateCounts(old.GetAll())
		dropped = queued
		old.Pause()
		old.CancelAll()
		old.RemoveAll()
	}
	_ = db.UpdateTaskData(&model.TaskItem{Key: "thumb_upload", PersistData: "[]"})
	thumbUploadManager = newThumbUploadManager(!thumbUploadPaused.Load())
	thumbUploadManagerMu.Unlock()
	thumbUploadMu.Lock()
	thumbUploadResetRoundLocked()
	thumbUploadMu.Unlock()
	thumbUploadStorageStates.Range(func(key, _ interface{}) bool {
		thumbUploadStorageStates.Delete(key)
		return true
	})
	common.SuccessResp(c, gin.H{"dropped": dropped})
}
