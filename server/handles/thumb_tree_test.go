package handles

import "testing"

func TestMergeThumbTreeFloorPreventsPartialCountRegression(t *testing.T) {
	current := []*thumbTreeNode{{
		Path: "/115", Name: "115", Videos: 1200, Cached: 819, Local: 700, Cloud: 819,
		Children: []*thumbTreeNode{{Path: "/115/reached", Name: "reached", Videos: 10, Cached: 8}},
	}}
	floor := []*thumbTreeNode{{
		Path: "/115", Name: "115", Videos: 1500, Cached: 1188, Local: 862, Cloud: 1188,
		Children: []*thumbTreeNode{{Path: "/115/known-only", Name: "known-only", Videos: 4, Cached: 4}},
	}}

	merged := mergeThumbTreeFloor(current, floor)
	if len(merged) != 1 {
		t.Fatalf("root count = %d, want 1", len(merged))
	}
	root := merged[0]
	if root.Videos != 1500 || root.Cached != 1188 || root.Local != 862 || root.Cloud != 1188 {
		t.Fatalf("merged root = videos:%d cached:%d local:%d cloud:%d", root.Videos, root.Cached, root.Local, root.Cloud)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children = %d, want reached + DB-known branch", len(root.Children))
	}
}
