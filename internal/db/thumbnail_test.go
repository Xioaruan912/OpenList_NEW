package db

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupThumbnailTestDB(t *testing.T) {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })

	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := testDB.AutoMigrate(&model.ThumbnailRecord{}); err != nil {
		t.Fatalf("migrate thumbnail record: %v", err)
	}
	db = testDB
}

func TestThumbnailIdentityPreservesIndexStateAndDetectsChange(t *testing.T) {
	setupThumbnailTestDB(t)
	record := model.ThumbnailRecord{
		PathKey:     "path-key",
		Kind:        "video",
		Path:        "/movie/a.mp4",
		Fingerprint: "fingerprint-a",
		CacheKey:    "cache-a",
		RemoteName:  "remote-a.png",
		ObjectID:    "object-a",
		Size:        100,
	}
	if err := SetThumbnailIndexed(record, true); err != nil {
		t.Fatalf("mark indexed: %v", err)
	}
	if err := SetThumbnailCloud(record, true); err != nil {
		t.Fatalf("mark cloud: %v", err)
	}
	changed, err := UpsertThumbnailIdentity(&record)
	if err != nil {
		t.Fatalf("upsert identity: %v", err)
	}
	if changed {
		t.Fatal("initial identity should not be classified as a content change")
	}

	record.Fingerprint = "fingerprint-b"
	record.CacheKey = "cache-b"
	record.RemoteName = "remote-b.png"
	record.ObjectID = "object-b"
	changed, err = UpsertThumbnailIdentity(&record)
	if err != nil {
		t.Fatalf("update identity: %v", err)
	}
	if !changed {
		t.Fatal("fingerprint change should be detected")
	}
	got, err := GetThumbnailRecord(record.PathKey)
	if err != nil {
		t.Fatalf("get thumbnail: %v", err)
	}
	if !got.Indexed || !got.Cloud {
		t.Fatalf("identity update lost state: indexed=%v cloud=%v", got.Indexed, got.Cloud)
	}
	if got.CacheKey != "cache-b" || got.RemoteName != "remote-b.png" {
		t.Fatalf("identity keys not updated: cache=%q remote=%q", got.CacheKey, got.RemoteName)
	}
}

func TestReplaceIndexedThumbnailPaths(t *testing.T) {
	setupThumbnailTestDB(t)
	first := model.ThumbnailRecord{PathKey: "a", Kind: "video", Path: "/a.mp4", CacheKey: "cache-a"}
	second := model.ThumbnailRecord{PathKey: "b", Kind: "video", Path: "/b.mp4", CacheKey: "cache-b"}
	if err := SetThumbnailIndexed(first, true); err != nil {
		t.Fatal(err)
	}
	if err := SetThumbnailIndexed(second, true); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceIndexedThumbnailPaths("video", []model.ThumbnailRecord{second}); err != nil {
		t.Fatalf("replace index: %v", err)
	}
	paths, err := ListIndexedThumbnailPaths("video")
	if err != nil {
		t.Fatalf("list index: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/b.mp4" {
		t.Fatalf("indexed paths = %#v, want [/b.mp4]", paths)
	}
}

func TestMoveThumbnailRecordPreservesContentKeys(t *testing.T) {
	setupThumbnailTestDB(t)
	record := model.ThumbnailRecord{
		PathKey:     "old-key",
		Kind:        "video",
		Path:        "/old/a.mp4",
		Fingerprint: "fingerprint-a",
		CacheKey:    "cache-a",
		RemoteName:  "remote-a.png",
		ObjectID:    "object-a",
		Size:        123,
		Indexed:     true,
		Cloud:       true,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	moved, err := MoveThumbnailRecord("video", "old-key", "new-key", "/new/a.mp4")
	if err != nil {
		t.Fatalf("move record: %v", err)
	}
	if !moved {
		t.Fatal("expected record move")
	}
	got, err := GetThumbnailRecord("new-key")
	if err != nil {
		t.Fatalf("get moved record: %v", err)
	}
	if got.Path != "/new/a.mp4" || got.Fingerprint != record.Fingerprint || got.CacheKey != record.CacheKey || got.RemoteName != record.RemoteName {
		t.Fatalf("moved record lost identity: %#v", got)
	}
	if !got.Indexed || !got.Cloud {
		t.Fatalf("moved record lost state: indexed=%v cloud=%v", got.Indexed, got.Cloud)
	}
	if _, err := GetThumbnailRecord("old-key"); err == nil {
		t.Fatal("old record still exists after move")
	}
}

func TestReplaceExcludedThumbnailPaths(t *testing.T) {
	setupThumbnailTestDB(t)
	first := model.ThumbnailRecord{PathKey: "a", Kind: "video", Path: "/a.mp4"}
	second := model.ThumbnailRecord{PathKey: "b", Kind: "video", Path: "/b.mp4"}
	if err := SetThumbnailExcluded(first, true); err != nil {
		t.Fatal(err)
	}
	if err := SetThumbnailExcluded(second, true); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceExcludedThumbnailPaths("video", []model.ThumbnailRecord{second}); err != nil {
		t.Fatalf("replace excluded: %v", err)
	}
	paths, err := ListExcludedThumbnailPaths("video")
	if err != nil {
		t.Fatalf("list excluded: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/b.mp4" {
		t.Fatalf("excluded paths = %#v, want [/b.mp4]", paths)
	}
}

func TestThumbnailFailureAndGenerationLifecycle(t *testing.T) {
	setupThumbnailTestDB(t)
	record := model.ThumbnailRecord{PathKey: "failure-key", Kind: "video", Path: "/movie/fail.mp4"}
	if err := SetThumbnailIndexed(record, false); err != nil {
		t.Fatalf("create thumbnail row: %v", err)
	}
	failedAt := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	retryAfter := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	if err := SetThumbnailFailure(record.PathKey, "timeout", "timed out", failedAt, retryAfter); err != nil {
		t.Fatalf("set failure: %v", err)
	}
	got, err := GetThumbnailRecord(record.PathKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureClass != "timeout" || got.FailureCount != 1 || got.FailureMessage != "timed out" {
		t.Fatalf("unexpected failure state: %#v", got)
	}
	if err := SetThumbnailFailure(record.PathKey, "timeout", "timed out again", failedAt, retryAfter); err != nil {
		t.Fatal(err)
	}
	got, _ = GetThumbnailRecord(record.PathKey)
	if got.FailureCount != 2 {
		t.Fatalf("failure count = %d, want 2", got.FailureCount)
	}
	generatedAt := time.Now().Truncate(time.Millisecond)
	if err := MarkThumbnailGenerated(record.PathKey, generatedAt, 1234); err != nil {
		t.Fatalf("mark generated: %v", err)
	}
	got, _ = GetThumbnailRecord(record.PathKey)
	if got.FailureClass != "" || got.FailureMessage != "" || !got.RetryAfter.IsZero() {
		t.Fatalf("generation did not clear failure state: %#v", got)
	}
	if got.GenerateCount != 1 || got.LastGenerateMS != 1234 {
		t.Fatalf("unexpected generation metrics: count=%d ms=%d", got.GenerateCount, got.LastGenerateMS)
	}
}
