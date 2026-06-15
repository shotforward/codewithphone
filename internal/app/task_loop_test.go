package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

type fakeTurnRunner struct {
	calls chan taskDispatch
}

func (f fakeTurnRunner) RunTurn(_ context.Context, dispatch taskDispatch, providerSessionRef string, _ turnExecutionProfile) (string, error) {
	if providerSessionRef != "" {
		return providerSessionRef, nil
	}
	f.calls <- dispatch
	return "thr_test_001", nil
}

type recordingTurnRunner struct {
	calls chan turnExecutionCall
}

type turnExecutionCall struct {
	Dispatch           taskDispatch
	ProviderSessionRef string
	Profile            turnExecutionProfile
}

func (r recordingTurnRunner) RunTurn(_ context.Context, dispatch taskDispatch, providerSessionRef string, profile turnExecutionProfile) (string, error) {
	r.calls <- turnExecutionCall{Dispatch: dispatch, ProviderSessionRef: providerSessionRef, Profile: profile}
	if providerSessionRef != "" {
		return providerSessionRef, nil
	}
	return "thr_test_resume", nil
}

type agentSessionRecordingRunner struct {
	calls chan turnExecutionCall
}

func (r agentSessionRecordingRunner) RunTurn(_ context.Context, dispatch taskDispatch, providerSessionRef string, profile turnExecutionProfile) (string, error) {
	r.calls <- turnExecutionCall{Dispatch: dispatch, ProviderSessionRef: providerSessionRef, Profile: profile}
	if providerSessionRef != "" {
		return providerSessionRef, nil
	}
	if dispatch.AgentSessionID != "" {
		return "thr_" + dispatch.AgentSessionID, nil
	}
	return "thr_group", nil
}

func TestRunTaskLoopClaimsTaskAndInvokesRunner(t *testing.T) {
	var claimed atomic.Bool
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/machines/machine-test/tasks/claim":
			if !claimed.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(taskDispatch{
				TaskRunID:     "task_001",
				SessionID:     "sess_001",
				Runtime:       "codex_cli",
				WorkspaceRoot: workspaceRoot,
				Prompt:        "Run tests",
			})
		case "/v1/machines/machine-test/events":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	calls := make(chan taskDispatch, 1)
	svc := &Service{
		cfg:              configFixture(),
		providerSessions: map[string]string{},
		serverClient: serverClient{
			BaseURL:    server.URL,
			MachineID:  "machine-test",
			HTTPClient: server.Client(),
		},
		codexRunner:  fakeTurnRunner{calls: calls},
		pollInterval: 5 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.runTaskLoop(ctx)
	}()

	select {
	case dispatch := <-calls:
		if dispatch.TaskRunID != "task_001" || dispatch.Runtime != "codex_cli" || dispatch.WorkspaceRoot != workspaceRoot {
			t.Fatalf("unexpected dispatch: %+v", dispatch)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for runner invocation")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			t.Fatalf("unexpected runTaskLoop error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task loop shutdown")
	}

	if got := svc.getProviderSession("sess_001"); got != "thr_test_001" {
		t.Fatalf("expected provider session ref to be stored, got %q", got)
	}
}

func TestHandleCodexDispatchReusesStoredProviderSession(t *testing.T) {
	calls := make(chan turnExecutionCall, 2)
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/machines/machine-test/events" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	svc := &Service{
		cfg:              configFixture(),
		providerSessions: map[string]string{},
		serverClient: serverClient{
			BaseURL:    server.URL,
			MachineID:  "machine-test",
			HTTPClient: server.Client(),
		},
		codexRunner: recordingTurnRunner{calls: calls},
		changeSets: changeSetClient{
			BaseURL:    server.URL,
			HTTPClient: server.Client(),
		},
	}

	firstDispatch := taskDispatch{
		TaskRunID:     "task_001",
		SessionID:     "sess_001",
		Runtime:       "codex_cli",
		WorkspaceRoot: workspaceRoot,
		Prompt:        "请修改 README.md",
	}
	if err := svc.handleCodexDispatch(context.Background(), firstDispatch); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	if got := <-calls; got.ProviderSessionRef != "" {
		t.Fatalf("expected first dispatch to start without provider session ref, got %q", got.ProviderSessionRef)
	} else if got.Profile.ReadOnly {
		t.Fatal("expected edit prompt to stay writable")
	}

	secondDispatch := taskDispatch{
		TaskRunID:     "task_002",
		SessionID:     "sess_001",
		Runtime:       "codex_cli",
		WorkspaceRoot: workspaceRoot,
		Prompt:        "只读，不要修改文件，帮我介绍下这个项目",
	}
	if err := svc.handleCodexDispatch(context.Background(), secondDispatch); err != nil {
		t.Fatalf("second dispatch failed: %v", err)
	}
	if got := <-calls; got.ProviderSessionRef != "thr_test_resume" {
		t.Fatalf("expected second dispatch to reuse stored provider session ref, got %q", got.ProviderSessionRef)
	} else if !got.Profile.ReadOnly {
		t.Fatal("expected project introduction prompt to run in read-only mode")
	}
}

func TestHandleCodexDispatchScopesProviderSessionByAgentSession(t *testing.T) {
	calls := make(chan turnExecutionCall, 3)
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/machines/machine-test/events" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	svc := &Service{
		cfg:              configFixture(),
		providerSessions: map[string]string{},
		serverClient: serverClient{
			BaseURL:    server.URL,
			MachineID:  "machine-test",
			HTTPClient: server.Client(),
		},
		codexRunner: agentSessionRecordingRunner{calls: calls},
	}

	firstPM := taskDispatch{
		TaskRunID:      "task_pm_001",
		SessionID:      "sess_001",
		AgentSessionID: "asess_pm",
		AgentID:        "pm",
		Runtime:        "codex_cli",
		WorkspaceRoot:  workspaceRoot,
		Prompt:         "只读，不要修改文件，帮我介绍下这个项目",
	}
	if err := svc.handleCodexDispatch(context.Background(), firstPM); err != nil {
		t.Fatalf("first PM dispatch failed: %v", err)
	}
	if got := <-calls; got.ProviderSessionRef != "" || got.Dispatch.AgentSessionID != "asess_pm" {
		t.Fatalf("expected first PM dispatch to start a new agent provider session, got %+v", got)
	}

	dev := taskDispatch{
		TaskRunID:      "task_dev_001",
		SessionID:      "sess_001",
		AgentSessionID: "asess_dev",
		AgentID:        "dev",
		Runtime:        "codex_cli",
		WorkspaceRoot:  workspaceRoot,
		Prompt:         "只读，不要修改文件，帮我介绍下这个项目",
	}
	if err := svc.handleCodexDispatch(context.Background(), dev); err != nil {
		t.Fatalf("dev dispatch failed: %v", err)
	}
	if got := <-calls; got.ProviderSessionRef != "" || got.Dispatch.AgentSessionID != "asess_dev" {
		t.Fatalf("expected dev dispatch to start a separate agent provider session, got %+v", got)
	}

	secondPM := firstPM
	secondPM.TaskRunID = "task_pm_002"
	if err := svc.handleCodexDispatch(context.Background(), secondPM); err != nil {
		t.Fatalf("second PM dispatch failed: %v", err)
	}
	if got := <-calls; got.ProviderSessionRef != "thr_asess_pm" || got.Dispatch.AgentSessionID != "asess_pm" {
		t.Fatalf("expected second PM dispatch to reuse PM provider session, got %+v", got)
	}

	if got := svc.getProviderSession("sess_001"); got != "" {
		t.Fatalf("expected v2 dispatches not to store provider session under group session, got %q", got)
	}
	if got := svc.getProviderSession("agent:asess_pm"); got != "thr_asess_pm" {
		t.Fatalf("expected PM provider session ref to be stored separately, got %q", got)
	}
	if got := svc.getProviderSession("agent:asess_dev"); got != "thr_asess_dev" {
		t.Fatalf("expected dev provider session ref to be stored separately, got %q", got)
	}
}

func TestHandleCodexDispatchUsesServerProviderSessionRefWhenLocalStateMissing(t *testing.T) {
	calls := make(chan turnExecutionCall, 1)
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/machines/machine-test/events" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	svc := &Service{
		cfg:              configFixture(),
		providerSessions: map[string]string{},
		serverClient: serverClient{
			BaseURL:    server.URL,
			MachineID:  "machine-test",
			HTTPClient: server.Client(),
		},
		codexRunner: recordingTurnRunner{calls: calls},
	}

	dispatch := taskDispatch{
		TaskRunID:          "task_pm_001",
		SessionID:          "sess_001",
		AgentSessionID:     "asess_pm",
		AgentID:            "pm",
		ProviderSessionRef: "thr_from_server",
		Runtime:            "codex_cli",
		WorkspaceRoot:      workspaceRoot,
		Prompt:             "继续上一轮讨论",
	}
	if err := svc.handleCodexDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if got := <-calls; got.ProviderSessionRef != "thr_from_server" {
		t.Fatalf("expected server provider session ref to be used, got %q", got.ProviderSessionRef)
	}
	if got := svc.getProviderSession("agent:asess_pm"); got != "thr_from_server" {
		t.Fatalf("expected server provider session ref to be cached locally, got %q", got)
	}
}

func TestRunFSTaskLoopClaimsAndCompletesTask(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "subdir"), 0o755); err != nil {
		t.Fatalf("create workspace subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	t.Setenv("DAEMON_ALLOWED_ROOTS", workspaceRoot)

	var claimed atomic.Bool
	resultCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/machines/machine-test/fs/claim":
			if !claimed.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fsTaskDispatch{
				TaskID:    "fstask_001",
				MachineID: "machine-test",
				TaskType:  "list_directories",
				RequestJSON: json.RawMessage([]byte(`{
					"path": "` + workspaceRoot + `",
					"limit": 50
				}`)),
			})
		case "/v1/machines/machine-test/fs/fstask_001/result":
			defer r.Body.Close()
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode fs result payload: %v", err)
			}
			resultCh <- payload
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := &Service{
		cfg:          configFixture(),
		allowedRoots: []string{workspaceRoot},
		serverClient: serverClient{
			BaseURL:    server.URL,
			MachineID:  "machine-test",
			HTTPClient: server.Client(),
		},
		pollInterval: 5 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.runFSTaskLoop(ctx)
	}()

	select {
	case payload := <-resultCh:
		if payload["status"] != "completed" {
			t.Fatalf("expected fs task completion status, got %+v", payload)
		}
		resultRaw, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("expected result payload object, got %+v", payload["result"])
		}
		items, ok := resultRaw["items"].([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("expected directory items in result, got %+v", resultRaw["items"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for fs task completion")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			t.Fatalf("unexpected runFSTaskLoop error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fs task loop shutdown")
	}
}

func configFixture() config.Config {
	return config.Config{
		HTTPAddr:           "127.0.0.1:0",
		MachineID:          "machine-test",
		MaxConcurrentTurns: 1,
		SQLitePath:         "./data/test.db",
		ServerBaseURL:      "http://example.invalid",
		ServerWSURL:        "ws://example.invalid/ws",
		CodexBin:           "codex",
	}
}

func TestMaxConcurrentTurnsFallback(t *testing.T) {
	svc := &Service{}
	if got := svc.maxConcurrentTurns(); got != 1 {
		t.Fatalf("expected fallback max concurrent turns to be 1, got %d", got)
	}

	svc = &Service{cfg: config.Config{MaxConcurrentTurns: 10}}
	if got := svc.maxConcurrentTurns(); got != 10 {
		t.Fatalf("expected configured max concurrent turns to be 10, got %d", got)
	}
}
