package changeset

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildChangeSetAndRestoreWorkspaceSnapshot(t *testing.T) {
	workspace := t.TempDir()

	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatalf("write old.txt: %v", err)
	}

	snapshot, err := CreateSnapshot(workspace)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	defer snapshot.Cleanup()

	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\nchanged\n"), 0o644); err != nil {
		t.Fatalf("mutate README: %v", err)
	}
	if err := os.Remove(filepath.Join(workspace, "old.txt")); err != nil {
		t.Fatalf("remove old.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	changeSet, err := BuildChangeSet("task_001", snapshot, workspace)
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	if changeSet == nil {
		t.Fatal("expected changeset to be generated")
	}
	if changeSet.ChangedFileCount != 3 {
		t.Fatalf("expected 3 changed files, got %d", changeSet.ChangedFileCount)
	}

	statusByPath := map[string]string{}
	for _, file := range changeSet.Files {
		statusByPath[file.Path] = file.Status
	}
	if statusByPath["README.md"] != "modified" {
		t.Fatalf("expected README.md to be modified, got %q", statusByPath["README.md"])
	}
	if statusByPath["old.txt"] != "deleted" {
		t.Fatalf("expected old.txt to be deleted, got %q", statusByPath["old.txt"])
	}
	if statusByPath["new.txt"] != "added" {
		t.Fatalf("expected new.txt to be added, got %q", statusByPath["new.txt"])
	}

	if err := snapshot.Restore(workspace); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}

	readme, err := os.ReadFile(filepath.Join(workspace, "README.md"))
	if err != nil {
		t.Fatalf("read restored README: %v", err)
	}
	if string(readme) != "hello\n" {
		t.Fatalf("unexpected restored README content: %q", string(readme))
	}

	oldFile, err := os.ReadFile(filepath.Join(workspace, "old.txt"))
	if err != nil {
		t.Fatalf("read restored old.txt: %v", err)
	}
	if string(oldFile) != "delete me\n" {
		t.Fatalf("unexpected restored old.txt content: %q", string(oldFile))
	}

	if _, err := os.Stat(filepath.Join(workspace, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected new.txt to be removed after restore, got err=%v", err)
	}
}

func TestBuildChangeSetReturnsNilWhenWorkspaceIsUnchanged(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("same\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	snapshot, err := CreateSnapshot(workspace)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	defer snapshot.Cleanup()

	changeSet, err := BuildChangeSet("task_001", snapshot, workspace)
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	if changeSet != nil {
		t.Fatalf("expected nil changeset for unchanged workspace, got %+v", changeSet)
	}
}

func TestSummarizeChangeSet(t *testing.T) {
	summary := SummarizeChangeSet([]File{{Path: "README.md", Status: "modified"}})
	if !strings.Contains(summary, "README.md") {
		t.Fatalf("expected summary to mention file path, got %q", summary)
	}
}

func TestApplySelectiveWorkspaceDecision(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "remove.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write remove.txt: %v", err)
	}

	snapshot, err := CreateSnapshot(workspace)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	defer snapshot.Cleanup()

	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("mutate README: %v", err)
	}
	if err := os.Remove(filepath.Join(workspace, "remove.txt")); err != nil {
		t.Fatalf("delete remove.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	changeSet, err := BuildChangeSet("task_001", snapshot, workspace)
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	if changeSet == nil {
		t.Fatal("expected changeset")
	}

	err = ApplySelectiveDecision(snapshot, workspace, changeSet.Files, []FileDecision{
		{Path: "README.md", Decision: "discard"},
		{Path: "remove.txt", Decision: "keep"},
		{Path: "new.txt", Decision: "discard"},
	})
	if err != nil {
		t.Fatalf("apply selective decision: %v", err)
	}

	readme, err := os.ReadFile(filepath.Join(workspace, "README.md"))
	if err != nil {
		t.Fatalf("read README after selective apply: %v", err)
	}
	if string(readme) != "base\n" {
		t.Fatalf("expected README restored, got %q", string(readme))
	}
	if _, err := os.Stat(filepath.Join(workspace, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected new.txt removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "remove.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected remove.txt to remain deleted for keep decision, got err=%v", err)
	}
}

func TestFilterChangeSetFilesSkipsGeneratedArtifacts(t *testing.T) {
	files := []File{
		{Path: ".cache/go-build/abc123.tmp", Status: "modified", Diff: "cache diff"},
		{Path: "__pycache__/app.cpython-311.pyc", Status: "added", Diff: "pyc diff"},
		{Path: "README.md", Status: "modified", Diff: "@@ -1 +1 @@\n-old\n+new\n"},
	}
	filtered := FilterFiles(files)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 meaningful file after filtering, got %d", len(filtered))
	}
	if filtered[0].Path != "README.md" {
		t.Fatalf("expected README.md to remain, got %+v", filtered)
	}
}

func TestBuildChangeSetSkipsTrackedGeneratedArtifactsInGitAwareMode(t *testing.T) {
	workspace := t.TempDir()
	if err := runGitCommand(workspace, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGitCommand(workspace, "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := runGitCommand(workspace, "config", "user.name", "PocketCode Test"); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}

	artifactPath := filepath.Join(workspace, ".cache", "go-build", "tracked.txt")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("tracked cache\n"), 0o644); err != nil {
		t.Fatalf("write tracked cache file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	if err := runGitCommand(workspace, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := runGitCommand(workspace, "commit", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	snapshot, err := CreateSnapshot(workspace)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	defer snapshot.Cleanup()

	if err := os.WriteFile(artifactPath, []byte("cache changed\n"), 0o644); err != nil {
		t.Fatalf("update tracked cache file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello changed\n"), 0o644); err != nil {
		t.Fatalf("update readme: %v", err)
	}

	changeSet, err := BuildChangeSet("task_001", snapshot, workspace)
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	if changeSet == nil {
		t.Fatal("expected changeset to be generated")
	}
	if changeSet.ChangedFileCount != 1 {
		t.Fatalf("expected only README.md in changeset, got %d files: %+v", changeSet.ChangedFileCount, changeSet.Files)
	}
	if changeSet.Files[0].Path != "README.md" {
		t.Fatalf("expected README.md to remain, got %+v", changeSet.Files)
	}
}

func TestBuildFileDiffIncludesHunkForLargeFileTailChange(t *testing.T) {
	beforeRoot := t.TempDir()
	afterRoot := t.TempDir()

	lines := make([]string, 22000)
	for i := range lines {
		lines[i] = "line " + strings.Repeat("x", 40)
	}
	beforeContent := strings.Join(lines, "\n") + "\n"
	lines[len(lines)-1] = "line changed at tail " + strings.Repeat("y", 40)
	afterContent := strings.Join(lines, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(beforeRoot, "big.txt"), []byte(beforeContent), 0o644); err != nil {
		t.Fatalf("write before file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afterRoot, "big.txt"), []byte(afterContent), 0o644); err != nil {
		t.Fatalf("write after file: %v", err)
	}

	diff, err := BuildFileDiff(beforeRoot, afterRoot, "big.txt", "modified")
	if err != nil {
		t.Fatalf("build file diff: %v", err)
	}
	if !strings.Contains(diff, "@@") {
		t.Fatalf("expected unified diff hunk for large tail change, got diff: %s", diff)
	}
	if !strings.Contains(diff, "-line "+strings.Repeat("x", 40)) {
		t.Fatalf("expected removed line in diff, got diff: %s", diff)
	}
	if !strings.Contains(diff, "+line changed at tail "+strings.Repeat("y", 40)) {
		t.Fatalf("expected added line in diff, got diff: %s", diff)
	}
}

func runGitCommand(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=PocketCode Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=PocketCode Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}
