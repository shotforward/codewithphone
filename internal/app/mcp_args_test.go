package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseRunCommandToolArgs_PrimaryKeys(t *testing.T) {
	raw := json.RawMessage(`{"command":"ls -la","cwd":"/tmp/demo","reason":"inspect"}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.RawCommand != "ls -la" {
		t.Fatalf("expected command ls -la, got %q", parsed.RawCommand)
	}
	if parsed.CWD != "/tmp/demo" {
		t.Fatalf("expected cwd /tmp/demo, got %q", parsed.CWD)
	}
	if parsed.Reason != "inspect" {
		t.Fatalf("expected reason inspect, got %q", parsed.Reason)
	}
	if parsed.CommandSource != "command" {
		t.Fatalf("expected source command, got %q", parsed.CommandSource)
	}
	if parsed.ExecutionMode != runCommandExecutionModeWait {
		t.Fatalf("expected execution mode wait, got %q", parsed.ExecutionMode)
	}
	if parsed.WaitTimeoutSec != 0 {
		t.Fatalf("expected wait timeout 0, got %d", parsed.WaitTimeoutSec)
	}
}

func TestParseRunCommandToolArgs_AliasKeys(t *testing.T) {
	raw := json.RawMessage(`{"cmd":"git status","workdir":"repo","justification":"check tree"}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.RawCommand != "git status" {
		t.Fatalf("expected command git status, got %q", parsed.RawCommand)
	}
	if parsed.CWD != "repo" {
		t.Fatalf("expected cwd repo, got %q", parsed.CWD)
	}
	if parsed.Reason != "check tree" {
		t.Fatalf("expected reason check tree, got %q", parsed.Reason)
	}
	if parsed.CommandSource != "cmd" {
		t.Fatalf("expected source cmd, got %q", parsed.CommandSource)
	}
}

func TestParseRunCommandToolArgs_UnwrapShellWrapper(t *testing.T) {
	raw := json.RawMessage(`{"command":"/bin/bash -lc \"git status --short\"","cwd":"."}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.RawCommand != "git status --short" {
		t.Fatalf("expected unwrapped command, got %q", parsed.RawCommand)
	}
}

func TestParseRunCommandToolArgs_ExecutableAndArgs(t *testing.T) {
	raw := json.RawMessage(`{"executable":"python3","args":["-c","print('PONG')"],"working_directory":"."}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !strings.HasPrefix(parsed.RawCommand, "python3 ") {
		t.Fatalf("expected command to start with python3, got %q", parsed.RawCommand)
	}
	if !strings.Contains(parsed.RawCommand, "-c") {
		t.Fatalf("expected command to contain -c, got %q", parsed.RawCommand)
	}
	if parsed.CWD != "." {
		t.Fatalf("expected cwd ., got %q", parsed.CWD)
	}
	if parsed.CommandSource != "executable+args" {
		t.Fatalf("expected source executable+args, got %q", parsed.CommandSource)
	}
}

func TestParseRunCommandToolArgs_StringPayload(t *testing.T) {
	raw := json.RawMessage(`"pwd"`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.RawCommand != "pwd" {
		t.Fatalf("expected command pwd, got %q", parsed.RawCommand)
	}
	if parsed.CWD != "." {
		t.Fatalf("expected cwd ., got %q", parsed.CWD)
	}
	if parsed.CommandSource != "string_argument" {
		t.Fatalf("expected source string_argument, got %q", parsed.CommandSource)
	}
	if parsed.ExecutionMode != runCommandExecutionModeWait {
		t.Fatalf("expected execution mode wait, got %q", parsed.ExecutionMode)
	}
}

func TestParseRunCommandToolArgs_AutoModeAndTimeout(t *testing.T) {
	raw := json.RawMessage(`{"command":"npm run dev","executionMode":"auto","waitTimeoutSec":120}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.ExecutionMode != runCommandExecutionModeAuto {
		t.Fatalf("expected execution mode auto, got %q", parsed.ExecutionMode)
	}
	if parsed.WaitTimeoutSec != 120 {
		t.Fatalf("expected wait timeout 120, got %d", parsed.WaitTimeoutSec)
	}
}

func TestParseRunCommandToolArgs_AutoModeDefaultTimeout(t *testing.T) {
	raw := json.RawMessage(`{"command":"npm run dev","executionMode":"auto"}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.WaitTimeoutSec != defaultAutoWaitTimeoutSec {
		t.Fatalf("expected default auto wait timeout %d, got %d", defaultAutoWaitTimeoutSec, parsed.WaitTimeoutSec)
	}
}

func TestParseRunCommandToolArgs_BackgroundAlias(t *testing.T) {
	raw := json.RawMessage(`{"command":"go run ./cmd/server","background":true}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.ExecutionMode != runCommandExecutionModeBackground {
		t.Fatalf("expected execution mode background, got %q", parsed.ExecutionMode)
	}
	if parsed.WaitTimeoutSec != 0 {
		t.Fatalf("expected wait timeout 0 for background mode, got %d", parsed.WaitTimeoutSec)
	}
}

func TestParseRunCommandToolArgs_InvalidModeFallsBackToWait(t *testing.T) {
	raw := json.RawMessage(`{"command":"pwd","executionMode":"unknown","timeoutSec":-5}`)
	parsed, err := parseRunCommandToolArgs(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.ExecutionMode != runCommandExecutionModeWait {
		t.Fatalf("expected execution mode wait fallback, got %q", parsed.ExecutionMode)
	}
	if parsed.WaitTimeoutSec != 1 {
		t.Fatalf("expected clamped wait timeout 1, got %d", parsed.WaitTimeoutSec)
	}
}

func TestParseRunCommandToolArgs_MissingCommand(t *testing.T) {
	raw := json.RawMessage(`{"cwd":"."}`)
	_, err := parseRunCommandToolArgs(raw)
	if err == nil {
		t.Fatalf("expected error for missing command")
	}
}
