package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteListWorkspaceEntries(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "beta"), 0o755); err != nil {
		t.Fatalf("mkdir beta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "zeta.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	svc := &Service{allowedRoots: []string{workspace}}
	first, err := svc.executeListWorkspaceEntries(workspace, "", 2, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Path != "" {
		t.Fatalf("expected root path, got %q", first.Path)
	}
	if len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("expected 2 items and hasMore=true, got len=%d hasMore=%v", len(first.Items), first.HasMore)
	}
	if first.Items[0].Kind != "directory" || first.Items[0].Path != "alpha" {
		t.Fatalf("unexpected first item: %+v", first.Items[0])
	}
	if first.Items[1].Kind != "directory" || first.Items[1].Path != "beta" {
		t.Fatalf("unexpected second item: %+v", first.Items[1])
	}

	second, err := svc.executeListWorkspaceEntries(workspace, "", 2, first.NextCursor)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.HasMore {
		t.Fatalf("expected no more entries on second page")
	}
	if len(second.Items) != 1 || second.Items[0].Kind != "file" || second.Items[0].Path != "zeta.txt" {
		t.Fatalf("unexpected second page entries: %+v", second.Items)
	}

	child, err := svc.executeListWorkspaceEntries(workspace, "alpha", 20, "")
	if err != nil {
		t.Fatalf("child page: %v", err)
	}
	if child.Path != "alpha" || child.ParentPath != "" {
		t.Fatalf("unexpected child navigation fields: path=%q parent=%q", child.Path, child.ParentPath)
	}
}

func TestExecuteListWorkspaceEntries_SymlinkDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "target"), 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace, "target"), filepath.Join(workspace, "linked")); err != nil {
		t.Fatalf("symlink linked -> target: %v", err)
	}

	svc := &Service{allowedRoots: []string{workspace}}
	resp, err := svc.executeListWorkspaceEntries(workspace, "", 20, "")
	if err != nil {
		t.Fatalf("list workspace entries: %v", err)
	}

	var linked *daemonWorkspaceEntry
	for i := range resp.Items {
		if resp.Items[i].Name == "linked" {
			linked = &resp.Items[i]
			break
		}
	}
	if linked == nil {
		t.Fatalf("linked entry missing: %+v", resp.Items)
	}
	if linked.Kind != "directory" {
		t.Fatalf("expected linked to be directory, got %q", linked.Kind)
	}
}
