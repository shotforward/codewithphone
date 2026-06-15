package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shotforward/codewithphone/internal/config"
)

func TestClaudeRunnerCompletesFromStreamedTextWhenResultIsEmpty(t *testing.T) {
	tmp := t.TempDir()
	claudeBin := filepath.Join(tmp, "claude-fake")
	script := `#!/usr/bin/env bash
printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude_sess"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}'
printf '%s\n' '{"type":"assistant","message":{"id":"msg_001","role":"assistant","content":[{"type":"text","text":"Hello world"}]}}'
printf '%s\n' '{"type":"result","session_id":"claude_sess","result":""}'
`
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	var (
		mu        sync.Mutex
		events    []daemonEvent
		decodeErr error
	)
	transport := testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var event daemonEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			mu.Lock()
			decodeErr = err
			mu.Unlock()
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(err.Error())),
				Header:     make(http.Header),
			}, nil
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})

	client := &serverClient{
		BaseURL:    "http://codewithphone.test",
		MachineID:  "machine_001",
		HTTPClient: &http.Client{Transport: transport},
	}
	runner := newClaudeRunner(config.Config{ClaudeBin: claudeBin}, client, func() string { return "http://127.0.0.1:1" }, nil)
	nextRef, err := runner.RunTurn(context.Background(), taskDispatch{
		TaskRunID:     "task_001",
		SessionID:     "sess_001",
		WorkspaceRoot: tmp,
		Prompt:        "@pm design",
	}, "", turnExecutionProfile{})
	if err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}
	if nextRef != "claude_sess" {
		t.Fatalf("nextRef = %q, want claude_sess", nextRef)
	}
	mu.Lock()
	defer mu.Unlock()
	if decodeErr != nil {
		t.Fatalf("decode event: %v", decodeErr)
	}

	var completed map[string]any
	for _, event := range events {
		if event.EventType != "assistant.message.completed" {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			t.Fatalf("completed payload type = %T", event.Payload)
		}
		completed = payload
	}
	if completed == nil {
		t.Fatal("assistant.message.completed not posted")
	}
	if completed["text"] != "Hello world" {
		t.Fatalf("completed text = %q, want Hello world", completed["text"])
	}
	if completed["itemId"] != "task_001:assistant:1" {
		t.Fatalf("completed itemId = %q, want stream item id", completed["itemId"])
	}
}
