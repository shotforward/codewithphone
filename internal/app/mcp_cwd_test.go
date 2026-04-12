package app

import "testing"

func TestResolveRunCommandExecCWD_UsesTaskWorkspaceRoot(t *testing.T) {
	svc := &Service{
		taskWorkspaces: map[string]string{
			"task_1": "/root/codes/ai-design",
		},
	}
	req := toolCallRequest{TaskRunID: "task_1"}

	got := svc.resolveRunCommandExecCWD(req, ".")
	if got != "/root/codes/ai-design" {
		t.Fatalf("expected workspace root cwd, got %q", got)
	}
}

func TestResolveRunCommandExecCWD_JoinsRelativeSubdir(t *testing.T) {
	svc := &Service{
		taskWorkspaces: map[string]string{
			"task_1": "/root/codes/ai-design",
		},
	}
	req := toolCallRequest{TaskRunID: "task_1"}

	got := svc.resolveRunCommandExecCWD(req, "server")
	if got != "/root/codes/ai-design/server" {
		t.Fatalf("expected joined cwd, got %q", got)
	}
}

func TestResolveRunCommandExecCWD_StripsAbsoluteInput(t *testing.T) {
	svc := &Service{
		taskWorkspaces: map[string]string{
			"task_1": "/root/codes/ai-design",
		},
	}
	req := toolCallRequest{TaskRunID: "task_1"}

	got := svc.resolveRunCommandExecCWD(req, "/tmp")
	if got != "/root/codes/ai-design" {
		t.Fatalf("absolute cwd should be normalized to workspace root, got %q", got)
	}
}

func TestTaskProfileLifecycle(t *testing.T) {
	svc := &Service{}
	taskRunID := "task_1"
	profile := turnExecutionProfile{ReadOnly: true, TrackChanges: false}

	svc.setTaskProfile(taskRunID, profile)
	got, ok := svc.getTaskProfile(taskRunID)
	if !ok {
		t.Fatal("expected task profile to exist")
	}
	if !got.ReadOnly || got.TrackChanges {
		t.Fatalf("unexpected stored profile: %+v", got)
	}

	svc.clearTaskProfile(taskRunID)
	if _, ok := svc.getTaskProfile(taskRunID); ok {
		t.Fatal("expected task profile to be cleared")
	}
}
