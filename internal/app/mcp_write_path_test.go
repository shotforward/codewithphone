package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWriteFileTargetPath(t *testing.T) {
	workspace := t.TempDir()
	svc := &Service{
		taskWorkspaces: map[string]string{
			"task_1": workspace,
		},
	}
	req := toolCallRequest{TaskRunID: "task_1"}

	got, err := svc.resolveWriteFileTargetPath(req, "sub/file.txt")
	if err != nil {
		t.Fatalf("resolve relative path failed: %v", err)
	}
	want := filepath.Join(workspace, "sub", "file.txt")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("unexpected resolved path: got %q want %q", got, want)
	}

	got, err = svc.resolveWriteFileTargetPath(req, want)
	if err != nil {
		t.Fatalf("resolve absolute in-workspace path failed: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("unexpected absolute path: got %q want %q", got, want)
	}

	if _, err := svc.resolveWriteFileTargetPath(req, "../escape.txt"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}

	outside := filepath.Join(filepath.Dir(workspace), "outside.txt")
	if _, err := svc.resolveWriteFileTargetPath(req, outside); err == nil {
		t.Fatal("expected out-of-workspace absolute path to be rejected")
	}
}

func TestExecuteWriteFileToolWritesIntoTaskWorkspace(t *testing.T) {
	workspace := t.TempDir()
	svc := &Service{
		taskWorkspaces: map[string]string{
			"task_1": workspace,
		},
	}

	args, _ := json.Marshal(map[string]any{
		"path":    "nested/a.txt",
		"content": "hello",
	})
	result := svc.executeWriteFileTool(context.Background(), toolCallRequest{
		TaskRunID: "task_1",
		Arguments: args,
	})
	if resultIsError(result) {
		t.Fatalf("expected write success, got error result: %#v", result)
	}

	target := filepath.Join(workspace, "nested", "a.txt")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written file failed: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("unexpected written content: %q", string(content))
	}
}
