package handles

import (
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// ThumbRuntime is the lightweight control-plane snapshot for the management UI. It deliberately
// avoids disk scans, remote listings and ffmpeg work: the UI can poll it frequently without turning
// observability into load on 115/OneDrive.
func ThumbRuntime(c *gin.Context) {
	thumbTreeSnapshotMu.RLock()
	treeStatus := thumbTreeSnapshotStatus
	treeAt := thumbTreeSnapshotAt
	thumbTreeSnapshotMu.RUnlock()
	if thumbTreeRefreshing.Load() {
		treeStatus = "refreshing"
	} else if treeStatus == "" {
		treeStatus = "cached"
	}
	refreshedAt := int64(0)
	if !treeAt.IsZero() {
		refreshedAt = treeAt.Unix()
	}

	common.SuccessResp(c, gin.H{
		"generation": gin.H{
			"queued":       thumbPrewarmQueued(),
			"active":       atomic.LoadInt32(&thumbActiveWorkers),
			"paused":       thumbQueuePaused.Load(),
			"blocked":      anyStorageBlocked(),
			"active_tasks": thumbActiveTasksSnapshot(),
		},
		"upload":         thumbUploadStatusSnapshot(),
		"candidate_jobs": thumbCandidateJobsSnapshot(),
		"tree": gin.H{
			"scan_status":  treeStatus,
			"refreshed_at": refreshedAt,
			"stale":        !treeAt.IsZero() && time.Since(treeAt) >= thumbTreeSnapshotTTL,
		},
	})
}
