package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldAutoDetachCommand(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{command: "npm run dev", want: true},
		{command: "go run ./cmd/server", want: true},
		{command: "docker compose up -d", want: true},
		{command: "tail -f /var/log/syslog", want: true},
		{command: "go test ./...", want: false},
		{command: "npm run build", want: false},
		{command: "ls -la", want: false},
	}
	for _, tc := range cases {
		got := shouldAutoDetachCommand(tc.command)
		if got != tc.want {
			t.Fatalf("command=%q expected %v got %v", tc.command, tc.want, got)
		}
	}
}

func TestReserveBackgroundCommandLimit(t *testing.T) {
	svc := &Service{
		backgroundCommands: map[string]backgroundCommandRun{},
	}
	sessionID := "sess_test"

	err := svc.reserveBackgroundCommand(backgroundCommandRun{
		CommandRunID: "cmd_1",
		SessionID:    sessionID,
	})
	if err != nil {
		t.Fatalf("first reserve failed: %v", err)
	}
	err = svc.reserveBackgroundCommand(backgroundCommandRun{
		CommandRunID: "cmd_2",
		SessionID:    sessionID,
	})
	if err != nil {
		t.Fatalf("second reserve failed: %v", err)
	}
	if err := svc.reserveBackgroundCommand(backgroundCommandRun{
		CommandRunID: "cmd_3",
		SessionID:    sessionID,
	}); err == nil {
		t.Fatal("expected third reserve to fail due to per-session limit")
	}

	svc.releaseBackgroundCommand("cmd_1")
	if err := svc.reserveBackgroundCommand(backgroundCommandRun{
		CommandRunID: "cmd_4",
		SessionID:    sessionID,
	}); err != nil {
		t.Fatalf("reserve after release failed: %v", err)
	}
}

func TestBuildCommandExecutionEnvOverridesCachePaths(t *testing.T) {
	t.Setenv("GOCACHE", "/root/codes/PocketCode/.cache/go-build")
	t.Setenv("GOMODCACHE", "/root/codes/PocketCode/.cache/go-mod")

	env, err := buildCommandExecutionEnv("cmd:test/001")
	if err != nil {
		t.Fatalf("buildCommandExecutionEnv failed: %v", err)
	}
	envMap := envMapFromList(env)
	cacheRoot := filepath.Join(os.TempDir(), runCommandCacheRootDirName, "cmd_test_001")

	expectPath := func(key, suffix string) {
		t.Helper()
		want := filepath.Join(cacheRoot, suffix)
		got := envMap[key]
		if got != want {
			t.Fatalf("%s mismatch: got=%q want=%q", key, got, want)
		}
		if _, err := os.Stat(got); err != nil {
			t.Fatalf("%s path not created: %v", key, err)
		}
	}

	expectPath("GOCACHE", "go-build")
	expectPath("GOMODCACHE", "go-mod")
	expectPath("TMPDIR", "tmp")
	expectPath("PYTHONPYCACHEPREFIX", "python-pyc")

	if strings.Contains(envMap["GOCACHE"], "/root/codes/PocketCode/.cache/go-build") {
		t.Fatalf("expected GOCACHE to be isolated from workspace, got=%q", envMap["GOCACHE"])
	}
}

func TestSanitizeCommandRunID(t *testing.T) {
	got := sanitizeCommandRunID("cmd:abc/def*ghi")
	if got != "cmd_abc_def_ghi" {
		t.Fatalf("unexpected sanitized id: %q", got)
	}
	if sanitizeCommandRunID("  ") != "cmd" {
		t.Fatal("empty id should fall back to cmd")
	}
}

func TestRunningCommandRegistryTerminate(t *testing.T) {
	svc := &Service{
		runningCommands: map[string]runningCommand{},
	}
	run := backgroundCommandRun{
		CommandRunID: "cmd_test_terminate",
		SessionID:    "sess_1",
		TaskRunID:    "task_1",
		PID:          123,
		StartedAt:    time.Now(),
	}
	exec := &commandExecution{}
	svc.registerRunningCommand(run, exec)

	gotRun, ok := svc.terminateRunningCommand("sess_1", "cmd_test_terminate")
	if !ok {
		t.Fatal("expected terminateRunningCommand to find command")
	}
	if gotRun.CommandRunID != run.CommandRunID || gotRun.PID != run.PID {
		t.Fatalf("unexpected run returned: %+v", gotRun)
	}
	svc.releaseRunningCommand("cmd_test_terminate")

	if _, ok := svc.terminateRunningCommand("sess_1", "cmd_test_terminate"); ok {
		t.Fatal("expected command to be absent after release")
	}
}

func TestExecuteTerminateCommandNotFound(t *testing.T) {
	svc := &Service{
		runningCommands: map[string]runningCommand{},
	}
	resp, err := svc.executeTerminateCommand("sess_1", "cmd_missing", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "not_found" {
		t.Fatalf("expected status not_found, got %q", resp.Status)
	}
}

func TestTerminateRunningCommandSessionMismatch(t *testing.T) {
	svc := &Service{
		runningCommands: map[string]runningCommand{},
	}
	run := backgroundCommandRun{
		CommandRunID: "cmd_test_session_guard",
		SessionID:    "sess_owner",
		PID:          456,
	}
	svc.registerRunningCommand(run, &commandExecution{})

	if _, ok := svc.terminateRunningCommand("sess_other", run.CommandRunID); ok {
		t.Fatal("expected terminateRunningCommand to reject different session")
	}
}
