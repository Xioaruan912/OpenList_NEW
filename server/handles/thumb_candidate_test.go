package handles

import (
	"context"
	"testing"
	"time"
)

func TestThumbCandidateSummaryOmitsImagePayload(t *testing.T) {
	job := &thumbCandidateJob{
		ID:      "job-1",
		Path:    "/media/example.mp4",
		State:   "succeeded",
		Done:    9,
		Total:   9,
		Created: time.Unix(123, 0),
		entry: thumbCandidateCacheEntry{
			frames: []thumbCandidateFrame{{index: 1, at: "3", png: []byte("image-bytes")}},
			sheet:  []byte("sheet-bytes"),
		},
	}

	summary := job.summary()
	if summary["job_id"] != "job-1" || summary["path"] != "/media/example.mp4" {
		t.Fatalf("unexpected summary identity: %#v", summary)
	}
	if summary["progress"] != float64(100) {
		t.Fatalf("progress = %#v, want 100", summary["progress"])
	}
	if _, ok := summary["candidates"]; ok {
		t.Fatal("summary must not contain candidate base64 payload")
	}
	if _, ok := summary["sheet"]; ok {
		t.Fatal("summary must not contain contact-sheet payload")
	}
}

func TestCancelQueuedThumbCandidateIsImmediate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	job := &thumbCandidateJob{
		ID:      "queued-job",
		Path:    "/media/example.mp4",
		State:   "queued",
		Created: time.Now(),
		cancel:  cancel,
	}

	thumbCandidateJobsMu.Lock()
	old := thumbCandidateJobs
	thumbCandidateJobs = map[string]*thumbCandidateJob{job.ID: job}
	thumbCandidateJobsMu.Unlock()
	t.Cleanup(func() {
		thumbCandidateJobsMu.Lock()
		thumbCandidateJobs = old
		thumbCandidateJobsMu.Unlock()
	})

	if !cancelThumbCandidateJob(job.ID) {
		t.Fatal("queued candidate was not found")
	}
	job.mu.RLock()
	state, errText := job.State, job.Err
	job.mu.RUnlock()
	if state != "canceled" {
		t.Fatalf("state = %q, want canceled", state)
	}
	if errText != context.Canceled.Error() {
		t.Fatalf("error = %q, want %q", errText, context.Canceled.Error())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("queued candidate context was not canceled")
	}
}
