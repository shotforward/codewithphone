package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestEmitTerminalEventRetriesUntilAccepted(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/machines/machine-test/events" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) < 3 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := serverClient{
		BaseURL:    server.URL,
		MachineID:  "machine-test",
		HTTPClient: server.Client(),
	}
	err := emitTerminalEvent(&client, daemonEvent{
		SessionID: "sess_001",
		TaskRunID: "task_001",
		EventType: "turn.completed",
		Payload:   map[string]any{"status": "completed"},
	})
	if err != nil {
		t.Fatalf("emitTerminalEvent() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestPostEventRetriesRetryableStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `{"error":"Deadlock found when trying to get lock"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := serverClient{
		BaseURL:    server.URL,
		MachineID:  "machine-test",
		HTTPClient: server.Client(),
	}
	if err := client.postEvent(context.Background(), daemonEvent{
		SessionID: "sess_001",
		TaskRunID: "task_001",
		EventType: "turn.heartbeat",
		Payload:   map[string]any{"phase": "finalizing"},
	}); err != nil {
		t.Fatalf("postEvent() error = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestPendingTerminalEventDrainReplaysAndRemoves(t *testing.T) {
	var received daemonEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "pending-terminal-events.jsonl")
	client := serverClient{
		BaseURL:                  server.URL,
		MachineID:                "machine-test",
		HTTPClient:               server.Client(),
		PendingTerminalEventPath: path,
	}
	event := daemonEvent{
		SessionID: "sess_001",
		TaskRunID: "task_001",
		EventType: "turn.completed",
		Payload:   map[string]any{"status": "completed"},
	}
	if err := client.enqueuePendingTerminalEvent(event, errors.New("temporary")); err != nil {
		t.Fatalf("enqueuePendingTerminalEvent() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pending file missing: %v", err)
	}
	sent, err := client.drainPendingTerminalEvents(context.Background())
	if err != nil {
		t.Fatalf("drainPendingTerminalEvents() error = %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}
	if received.EventType != "turn.completed" || received.TaskRunID != "task_001" || received.EventID == "" {
		t.Fatalf("unexpected replayed event: %+v", received)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending file still exists, stat err=%v", err)
	}
}
