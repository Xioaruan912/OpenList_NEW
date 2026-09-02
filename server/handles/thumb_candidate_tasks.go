package handles

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

const (
	thumbCandidateJobTTL     = 30 * time.Minute
	thumbCandidateQueueMax   = 16
	thumbCandidateHistoryMax = 32
)

var (
	thumbCandidateJobsMu   sync.Mutex
	thumbCandidateJobs     = make(map[string]*thumbCandidateJob)
	thumbCandidateQueue    = make(chan thumbCandidateQueueItem, thumbCandidateQueueMax)
	thumbCandidateWorkOnce sync.Once
)

type thumbCandidateQueueItem struct {
	job *thumbCandidateJob
	ctx context.Context
}

type ThumbCandidatesReq struct {
	Path    string `json:"path"`
	Refresh bool   `json:"refresh"`
}

type thumbCandidateJob struct {
	mu      sync.RWMutex
	ID      string
	Path    string
	State   string
	Done    int
	Total   int
	Err     string
	Created time.Time
	entry   thumbCandidateCacheEntry
	cancel  context.CancelFunc
}

func (j *thumbCandidateJob) snapshot() gin.H {
	j.mu.RLock()
	defer j.mu.RUnlock()
	progress := 0.0
	if j.Total > 0 {
		progress = float64(j.Done) * 100 / float64(j.Total)
	}
	out := gin.H{
		"job_id":     j.ID,
		"path":       j.Path,
		"state":      j.State,
		"done":       j.Done,
		"total":      j.Total,
		"progress":   progress,
		"error":      j.Err,
		"created_at": j.Created.Unix(),
	}
	if j.State == "succeeded" {
		for key, value := range thumbCandidateResponse(j.Path, j.entry, false) {
			out[key] = value
		}
	}
	return out
}

func (j *thumbCandidateJob) summary() gin.H {
	j.mu.RLock()
	defer j.mu.RUnlock()
	progress := 0.0
	if j.Total > 0 {
		progress = float64(j.Done) * 100 / float64(j.Total)
	}
	return gin.H{
		"job_id":     j.ID,
		"path":       j.Path,
		"state":      j.State,
		"done":       j.Done,
		"total":      j.Total,
		"progress":   progress,
		"error":      j.Err,
		"created_at": j.Created.Unix(),
	}
}

func thumbCandidateJobSetError(job *thumbCandidateJob, err error) {
	job.mu.Lock()
	if errors.Is(err, context.Canceled) {
		job.State = "canceled"
	} else {
		job.State = "failed"
	}
	if err != nil {
		job.Err = err.Error()
	}
	job.mu.Unlock()
}

func startThumbCandidateWorker() {
	thumbCandidateWorkOnce.Do(func() {
		go func() {
			for item := range thumbCandidateQueue {
				item.job.mu.RLock()
				state := item.job.State
				item.job.mu.RUnlock()
				if state == "canceled" {
					continue
				}
				if err := runThumbCandidateJob(item.job, item.ctx); err != nil {
					thumbCandidateJobSetError(item.job, err)
				}
			}
		}()
	})
}

func cleanupThumbCandidateJobsLocked(now time.Time) {
	for id, job := range thumbCandidateJobs {
		job.mu.RLock()
		state, created := job.State, job.Created
		job.mu.RUnlock()
		if state != "queued" && state != "running" && now.Sub(created) > thumbCandidateJobTTL {
			delete(thumbCandidateJobs, id)
		}
	}
	if len(thumbCandidateJobs) <= thumbCandidateHistoryMax {
		return
	}
	type completedJob struct {
		id      string
		created time.Time
	}
	completed := make([]completedJob, 0, len(thumbCandidateJobs))
	for id, job := range thumbCandidateJobs {
		job.mu.RLock()
		state, created := job.State, job.Created
		job.mu.RUnlock()
		if state != "queued" && state != "running" {
			completed = append(completed, completedJob{id: id, created: created})
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].created.Before(completed[j].created) })
	for _, job := range completed {
		if len(thumbCandidateJobs) <= thumbCandidateHistoryMax {
			break
		}
		delete(thumbCandidateJobs, job.id)
	}
}

func activeThumbCandidateJobsLocked() int {
	active := 0
	for _, job := range thumbCandidateJobs {
		job.mu.RLock()
		state := job.State
		job.mu.RUnlock()
		if state == "queued" || state == "running" {
			active++
		}
	}
	return active
}

func thumbCandidateJobsSnapshot() []gin.H {
	now := time.Now()
	thumbCandidateJobsMu.Lock()
	cleanupThumbCandidateJobsLocked(now)
	jobs := make([]*thumbCandidateJob, 0, len(thumbCandidateJobs))
	for _, job := range thumbCandidateJobs {
		jobs = append(jobs, job)
	}
	thumbCandidateJobsMu.Unlock()
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Created.After(jobs[j].Created) })
	items := make([]gin.H, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, job.summary())
	}
	return items
}

func cancelThumbCandidateJob(jobID string) bool {
	thumbCandidateJobsMu.Lock()
	job := thumbCandidateJobs[jobID]
	thumbCandidateJobsMu.Unlock()
	if job == nil {
		return false
	}
	job.mu.Lock()
	cancel := job.cancel
	if job.State == "queued" {
		job.State = "canceled"
		job.Err = context.Canceled.Error()
	}
	job.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// ThumbCandidates starts a backend-owned candidate job. Cached results return immediately;
// otherwise the caller may leave the page and reconnect through ThumbRuntime/ThumbCandidateStatus.
func ThumbCandidates(c *gin.Context) {
	var req ThumbCandidatesReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	rawPath := req.Path
	if rawPath != "" && !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	if rawPath == "" {
		common.ErrorStrResp(c, "invalid path", 400)
		return
	}
	if !req.Refresh {
		if entry, ok := thumbCandidateCacheGet(rawPath); ok {
			out := thumbCandidateResponse(rawPath, entry, true)
			out["state"] = "succeeded"
			out["progress"] = 100.0
			common.SuccessResp(c, out)
			return
		}
	}
	if blocked, _ := isStorageBlocked(rawPath); blocked {
		common.ErrorStrResp(c, "存储风控中，暂不能生成候选缩略图", 423)
		return
	}

	thumbCandidateJobsMu.Lock()
	cleanupThumbCandidateJobsLocked(time.Now())
	for _, existing := range thumbCandidateJobs {
		existing.mu.RLock()
		state, path := existing.State, existing.Path
		existing.mu.RUnlock()
		if path == rawPath && (state == "queued" || state == "running") {
			thumbCandidateJobsMu.Unlock()
			common.SuccessResp(c, existing.snapshot())
			return
		}
	}
	if activeThumbCandidateJobsLocked() >= thumbCandidateQueueMax {
		thumbCandidateJobsMu.Unlock()
		common.ErrorStrResp(c, "候选任务队列已满，请稍后再试", 429)
		return
	}
	jobID := strconv.FormatInt(time.Now().UnixNano(), 36)
	jobCtx, cancel := context.WithCancel(context.Background())
	job := &thumbCandidateJob{ID: jobID, Path: rawPath, State: "queued", Created: time.Now(), cancel: cancel}
	thumbCandidateJobs[jobID] = job
	thumbCandidateJobsMu.Unlock()

	startThumbCandidateWorker()
	select {
	case thumbCandidateQueue <- thumbCandidateQueueItem{job: job, ctx: jobCtx}:
	case <-jobCtx.Done():
		thumbCandidateJobSetError(job, jobCtx.Err())
	case <-time.After(200 * time.Millisecond):
		cancel()
		thumbCandidateJobsMu.Lock()
		delete(thumbCandidateJobs, jobID)
		thumbCandidateJobsMu.Unlock()
		common.ErrorStrResp(c, "候选任务队列已满，请稍后再试", 429)
		return
	}
	common.SuccessResp(c, job.snapshot())
}

func ThumbCandidateJobs(c *gin.Context) {
	common.SuccessResp(c, gin.H{"jobs": thumbCandidateJobsSnapshot()})
}

func ThumbCandidateStatus(c *gin.Context) {
	jobID := c.Query("job_id")
	thumbCandidateJobsMu.Lock()
	job := thumbCandidateJobs[jobID]
	thumbCandidateJobsMu.Unlock()
	if job == nil {
		common.ErrorStrResp(c, "candidate job not found", 404)
		return
	}
	common.SuccessResp(c, job.snapshot())
}

func ThumbCandidateCancel(c *gin.Context) {
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := c.ShouldBind(&req); err != nil || req.JobID == "" {
		common.ErrorStrResp(c, "invalid job_id", 400)
		return
	}
	if !cancelThumbCandidateJob(req.JobID) {
		common.ErrorStrResp(c, "candidate job not found", 404)
		return
	}
	common.SuccessResp(c, gin.H{"job_id": req.JobID, "canceled": true})
}
