package app

import (
	"testing"

	"github.com/shotforward/codewithphone/internal/config"
)

func TestNewSharesServerClientAcrossRunners(t *testing.T) {
	svc := New(config.Config{
		HTTPAddr:      "127.0.0.1:8081",
		MachineID:     "machine-test",
		SQLitePath:    "/tmp/daemon-test.db",
		ServerBaseURL: "http://127.0.0.1:8080",
		CodexBin:      "codex",
		GeminiBin:     "gemini",
		ClaudeBin:     "claude",
	})

	svc.serverClient.MachineToken = "machine-token-123"

	codex, ok := svc.codexRunner.(*codexRunner)
	if !ok || codex.server == nil {
		t.Fatalf("codex runner should keep a shared server client pointer")
	}
	if codex.server.MachineToken != "machine-token-123" {
		t.Fatalf("codex runner token mismatch: got %q", codex.server.MachineToken)
	}

	gemini, ok := svc.geminiRunner.(*geminiRunner)
	if !ok || gemini.server == nil {
		t.Fatalf("gemini runner should keep a shared server client pointer")
	}
	if gemini.server.MachineToken != "machine-token-123" {
		t.Fatalf("gemini runner token mismatch: got %q", gemini.server.MachineToken)
	}

	claude, ok := svc.claudeRunner.(*claudeRunner)
	if !ok || claude.server == nil {
		t.Fatalf("claude runner should keep a shared server client pointer")
	}
	if claude.server.MachineToken != "machine-token-123" {
		t.Fatalf("claude runner token mismatch: got %q", claude.server.MachineToken)
	}
}
