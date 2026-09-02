package handles

import (
	"context"
	"os"
	stdpath "path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Tree control/data bridge: the HTTP path serves a DB/snapshot-first tree immediately while the
// remote filesystem is reconciled asynchronously with an independent budget per mount.

type thumbTreeNode struct {
	Path     string           `json:"path"`
	Name     string           `json:"name"`
	Cached   int              `json:"cached"`
	Local    int              `json:"local"`
	Cloud    int              `json:"cloud"`
	Videos   int              `json:"videos"`
	Children []*thumbTreeNode `json:"children"`
}

const (
	thumbTreeScanTO      = 30 * time.Second
	thumbTreeReconcileTO = 2 * time.Minute
	thumbTreeSnapshotTTL = 5 * time.Minute
)

var (
	thumbTreeSnapshotMu     sync.RWMutex
	thumbTreeSnapshot       []*thumbTreeNode
	thumbTreeSnapshotAt     time.Time
	thumbTreeSnapshotStatus string
	thumbTreeRefreshing     atomic.Bool
	thumbIndexMigrateMu     sync.Mutex
)

func ThumbTree(c *gin.Context) {
	children, status, at := fastThumbTree()
	if time.Since(at) >= thumbTreeSnapshotTTL && thumbTreeRefreshing.CompareAndSwap(false, true) {
		status = "refreshing"
		go func() {
			defer thumbTreeRefreshing.Store(false)
			refreshed, scanStatus := scanThumbTreeRemote(context.Background())
			thumbTreeSnapshotMu.Lock()
			thumbTreeSnapshot = refreshed
			thumbTreeSnapshotAt = time.Now()
			thumbTreeSnapshotStatus = scanStatus
			thumbTreeSnapshotMu.Unlock()
			log.Infof("thumbnail tree background reconcile finished: status=%s roots=%d", scanStatus, len(refreshed))
		}()
	} else if thumbTreeRefreshing.Load() && status != "complete" {
		status = "refreshing"
	}
	refreshedAt := int64(0)
	if !at.IsZero() {
		refreshedAt = at.Unix()
	}
	common.SuccessResp(c, gin.H{"children": children, "scan_status": status, "refreshed_at": refreshedAt})
}

func cloneThumbTree(nodes []*thumbTreeNode) []*thumbTreeNode {
	out := make([]*thumbTreeNode, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		copyNode := *node
		copyNode.Children = cloneThumbTree(node.Children)
		out = append(out, &copyNode)
	}
	return out
}

func ensureThumbTreeDir(root *thumbTreeNode, dir string) *thumbTreeNode {
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	cur := root
	path := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		path += "/" + part
		var child *thumbTreeNode
		for _, existing := range cur.Children {
			if existing.Path == path {
				child = existing
				break
			}
		}
		if child == nil {
			child = &thumbTreeNode{Path: path, Name: part}
			cur.Children = append(cur.Children, child)
		}
		cur = child
	}
	return cur
}

func sortThumbTree(nodes []*thumbTreeNode) {
	sort.Slice(nodes, func(i, j int) bool { return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name) })
	for _, node := range nodes {
		sortThumbTree(node.Children)
	}
}

func sumThumbTree(nodes []*thumbTreeNode) (videos, cached, local, cloud int) {
	for _, node := range nodes {
		cv, cc, cl, ccl := sumThumbTree(node.Children)
		node.Videos += cv
		node.Cached += cc
		node.Local += cl
		node.Cloud += ccl
		videos += node.Videos
		cached += node.Cached
		local += node.Local
		cloud += node.Cloud
	}
	return
}

func buildThumbTreeFromDB() []*thumbTreeNode {
	root := &thumbTreeNode{}
	for _, mount := range currentMountPaths() {
		ensureThumbTreeDir(root, mount)
	}
	records, err := db.ListThumbnailRecords(thumbKindVideo)
	if err != nil {
		return root.Children
	}
	for _, record := range records {
		if record.Path == "" {
			continue
		}
		dir := stdpath.Dir(record.Path)
		if dir == "." || dir == "" || dir == "/" {
			continue
		}
		node := ensureThumbTreeDir(root, dir)
		node.Videos++
		localExists := false
		if record.CacheKey != "" || record.Fingerprint != "" || record.Indexed {
			if _, statErr := os.Stat(thumbCachePath(record.Kind, record.Path)); statErr == nil {
				localExists = true
				node.Local++
			}
		}
		if record.Cloud {
			node.Cloud++
		}
		if localExists || record.Cloud {
			node.Cached++
		}
	}
	_, cached, local, cloud := sumThumbTree(root.Children)
	thumbAggMu.Lock()
	thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = cached, local, cloud
	thumbAggAt = time.Now()
	thumbAggMu.Unlock()
	sortThumbTree(root.Children)
	return root.Children
}

func fastThumbTree() ([]*thumbTreeNode, string, time.Time) {
	thumbTreeSnapshotMu.RLock()
	if len(thumbTreeSnapshot) > 0 {
		nodes := cloneThumbTree(thumbTreeSnapshot)
		at := thumbTreeSnapshotAt
		status := thumbTreeSnapshotStatus
		thumbTreeSnapshotMu.RUnlock()
		if status == "" {
			status = "complete"
		}
		if time.Since(at) >= thumbTreeSnapshotTTL {
			status = "stale"
		}
		return nodes, status, at
	}
	thumbTreeSnapshotMu.RUnlock()
	return buildThumbTreeFromDB(), "cached", time.Time{}
}

func scanThumbTreeRemote(ctx context.Context) ([]*thumbTreeNode, string) {
	scanStarted := time.Now()
	reconcileCtx, reconcileCancel := context.WithTimeout(ctx, thumbTreeReconcileTO)
	defer reconcileCancel()
	indexed := readThumbIndex()
	cachedByDir := map[string]int{}
	localByDir := map[string]int{}
	cloudByDir := map[string]int{}
	cloud := readThumbCloudIndex()
	for _, path := range indexed {
		dir := stdpath.Dir(path)
		if dir != "" && dir != "." {
			exists := false
			if _, err := os.Stat(thumbCachePath(thumbKindVideo, path)); err == nil {
				localByDir[dir]++
				exists = true
			}
			if cloud[path] {
				cloudByDir[dir]++
				exists = true
			}
			if exists {
				cachedByDir[dir]++
			}
		}
	}

	root := &thumbTreeNode{}
	totalDirsCount := 0
	completeMounts := map[string]bool{}
	realDirs := map[string]bool{}
	indexedSet := map[string]bool{}
	for _, path := range indexed {
		indexedSet[path] = true
	}

	var scan func(context.Context, string, *thumbTreeNode, *int, *int, *bool)
	scan = func(scanCtx context.Context, dir string, cur *thumbTreeNode, dirsCount, scanFailed *int, scanTruncated *bool) {
		if scanCtx.Err() != nil {
			return
		}
		if *dirsCount >= thumbScanMaxDirs {
			*scanTruncated = true
			return
		}
		*dirsCount++
		realDirs[dir] = true
		objs, err := fs.List(scanCtx, dir, &fs.ListArgs{})
		if err != nil {
			*scanFailed++
			return
		}
		names := loadRemoteThumbListing(scanCtx, dir, folderNameOnly{thumbFolderNameForPath(dir)})
		for _, obj := range objs {
			if obj.IsDir() {
				if obj.GetName() == "_thumbnails" {
					continue
				}
				childPath := dir + "/" + obj.GetName()
				child := &thumbTreeNode{Path: childPath, Name: obj.GetName()}
				cur.Children = append(cur.Children, child)
				scan(scanCtx, childPath, child, dirsCount, scanFailed, scanTruncated)
				continue
			}
			if utils.GetFileType(obj.GetName()) != conf.VIDEO {
				continue
			}
			cur.Videos++
			rawPath := dir + "/" + obj.GetName()
			thumbRememberObject(thumbKindVideo, rawPath, obj)
			inCloud := names[remoteThumbName(rawPath)]
			if len(names) == 0 && !inCloud {
				inCloud = cloud[rawPath]
			}
			localExists := false
			if indexedSet[rawPath] {
				if _, err := os.Stat(thumbCachePath(thumbKindVideo, rawPath)); err == nil {
					localExists = true
				}
			}
			if inCloud || localExists {
				cur.Cached++
			}
			if inCloud {
				cur.Cloud++
			}
			if localExists {
				cur.Local++
			}
		}
	}

	mounts := currentMountPaths()
	for _, mount := range mounts {
		realDirs[mount] = true
		child := &thumbTreeNode{Path: mount, Name: strings.TrimPrefix(mount, "/")}
		root.Children = append(root.Children, child)
		if reconcileCtx.Err() != nil {
			continue
		}
		mountCtx, mountCancel := context.WithTimeout(reconcileCtx, thumbTreeScanTO)
		mountDirs, mountFailed := 0, 0
		mountTruncated := false
		scan(mountCtx, mount, child, &mountDirs, &mountFailed, &mountTruncated)
		mountTimedOut := mountCtx.Err() != nil
		mountCancel()
		totalDirsCount += mountDirs
		completeMounts[mount] = mountDirs > 0 && mountFailed == 0 && !mountTruncated && !mountTimedOut
	}

	status := "complete"
	if totalDirsCount == 0 || reconcileCtx.Err() != nil {
		status = "partial"
	} else {
		for _, mount := range mounts {
			if !completeMounts[mount] {
				status = "partial"
				break
			}
		}
	}

	if status == "complete" && autoMigrateThumbIndex(realDirs) > 0 {
		indexed = readThumbIndex()
		cachedByDir = map[string]int{}
		localByDir = map[string]int{}
		cloudByDir = map[string]int{}
		cloud = readThumbCloudIndex()
		for _, path := range indexed {
			dir := stdpath.Dir(path)
			if dir == "" || dir == "." {
				continue
			}
			cachedByDir[dir]++
			if _, err := os.Stat(thumbCachePath(thumbKindVideo, path)); err == nil {
				localByDir[dir]++
			}
			if cloud[path] {
				cloudByDir[dir]++
			}
		}
		refreshCtx, refreshCancel := context.WithTimeout(reconcileCtx, thumbTreeScanTO)
		defer refreshCancel()
		var refreshCached func([]*thumbTreeNode)
		refreshCached = func(nodes []*thumbTreeNode) {
			for _, node := range nodes {
				node.Cached = cachedByDir[node.Path]
				node.Local = localByDir[node.Path]
				node.Cloud = len(loadRemoteThumbListing(refreshCtx, node.Path, folderNameOnly{thumbFolderNameForPath(node.Path)}))
				refreshCached(node.Children)
			}
		}
		refreshCached(root.Children)
	}

	if records, err := db.ListThumbnailRecords(thumbKindVideo); err == nil {
		for _, record := range records {
			if !record.LastSeenAt.IsZero() && !record.LastSeenAt.Before(scanStarted) {
				continue
			}
			for mount, complete := range completeMounts {
				if !complete {
					continue
				}
				prefix := strings.TrimRight(mount, "/")
				if record.Path != mount && !strings.HasPrefix(record.Path, prefix+"/") {
					continue
				}
				_ = db.DeleteThumbnailRecord(record.PathKey)
				prewarmDone.Delete(record.Path)
				remoteThumbCacheDelete(record.Path)
				break
			}
		}
	}

	for dir, count := range cachedByDir {
		parts := strings.Split(strings.Trim(dir, "/"), "/")
		cur := root
		path := ""
		for _, part := range parts {
			if part == "" {
				continue
			}
			path += "/" + part
			var child *thumbTreeNode
			for _, existing := range cur.Children {
				if existing.Path == path {
					child = existing
					break
				}
			}
			if child == nil {
				child = &thumbTreeNode{Path: path, Name: part, Cached: count, Local: localByDir[dir], Cloud: cloudByDir[dir]}
				cur.Children = append(cur.Children, child)
			}
			cur = child
		}
	}

	var sumSubtree func(*thumbTreeNode) (int, int, int, int)
	sumSubtree = func(node *thumbTreeNode) (videos, cached, local, cloud int) {
		videos, cached, local, cloud = node.Videos, node.Cached, node.Local, node.Cloud
		for _, child := range node.Children {
			cv, cc, cl, ccl := sumSubtree(child)
			videos += cv
			cached += cc
			local += cl
			cloud += ccl
		}
		node.Videos, node.Cached, node.Local, node.Cloud = videos, cached, local, cloud
		return
	}
	for _, mount := range root.Children {
		sumSubtree(mount)
	}
	allCached, allLocal, allCloud := 0, 0, 0
	for _, mount := range root.Children {
		allCached += mount.Cached
		allLocal += mount.Local
		allCloud += mount.Cloud
	}
	thumbAggMu.Lock()
	thumbAgg.cached, thumbAgg.local, thumbAgg.cloud = allCached, allLocal, allCloud
	thumbAggAt = time.Now()
	thumbAggMu.Unlock()
	return root.Children, status
}

func autoMigrateThumbIndex(realDirs map[string]bool) int {
	if len(realDirs) == 0 {
		return 0
	}
	thumbIndexMigrateMu.Lock()
	defer thumbIndexMigrateMu.Unlock()
	paths := readThumbIndex()
	if len(paths) == 0 {
		return 0
	}
	dirMap := map[string]string{}
	migrated := 0
	changed := false
	var newPaths []string
	kinds := []string{thumbKindVideo, thumbKindAudio, thumbKindImage, thumbKindCover}
	for _, path := range paths {
		oldDir := stdpath.Dir(path)
		if oldDir == "" || oldDir == "." || realDirs[oldDir] {
			newPaths = append(newPaths, path)
			continue
		}
		newDir, ok := dirMap[oldDir]
		if !ok {
			name := stdpath.Base(oldDir)
			var candidates []string
			for dir := range realDirs {
				if stdpath.Base(dir) == name {
					candidates = append(candidates, dir)
				}
			}
			if len(candidates) == 1 {
				newDir = candidates[0]
			}
			dirMap[oldDir] = newDir
		}
		if newDir == "" {
			newPaths = append(newPaths, path)
			continue
		}
		newPath := newDir + strings.TrimPrefix(path, oldDir)
		moved := false
		for _, kind := range kinds {
			oldCache := thumbCachePath(kind, path)
			thumbMoveRecord(kind, path, newPath)
			newCache := thumbCachePath(kind, newPath)
			if oldCache != newCache {
				if _, err := os.Stat(oldCache); err == nil {
					if _, err := os.Stat(newCache); err != nil {
						if os.Rename(oldCache, newCache) == nil {
							migrated++
							moved = true
						}
					} else {
						_ = os.Remove(oldCache)
					}
				}
			}
			oldFail := thumbFailPath(kind, path)
			newFail := thumbFailPath(kind, newPath)
			if _, err := os.Stat(oldFail); err == nil {
				if _, err := os.Stat(newFail); err != nil {
					_ = os.Rename(oldFail, newFail)
				} else {
					_ = os.Remove(oldFail)
				}
			}
		}
		newPaths = append(newPaths, newPath)
		changed = changed || moved
	}
	if changed {
		_ = writeThumbIndex(newPaths)
		log.Infof("[thumb] auto-migrated %d thumbnail index entries", migrated)
	}
	return migrated
}
