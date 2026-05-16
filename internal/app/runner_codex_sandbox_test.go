package app

import "testing"

func TestThreadSandboxForProfile(t *testing.T) {
	if got := threadSandboxForProfile(turnExecutionProfile{}); got != "workspace-write" {
		t.Fatalf("expected non-read-only sandbox to be workspace-write, got %q", got)
	}
	if got := threadSandboxForProfile(turnExecutionProfile{ReadOnly: true}); got != "read-only" {
		t.Fatalf("expected read-only sandbox to be read-only, got %q", got)
	}
}

func TestApprovalPolicyForProfile(t *testing.T) {
	// Both profiles must request approval so the daemon-side policy engine
	// can adjudicate every command — Codex CLI emits requestApproval, the
	// daemon classifies and either auto-accepts or surfaces a card.
	if got := approvalPolicyForProfile(turnExecutionProfile{}); got != "on-request" {
		t.Fatalf("expected non-read-only approvalPolicy to be on-request, got %q", got)
	}
	if got := approvalPolicyForProfile(turnExecutionProfile{ReadOnly: true}); got != "on-request" {
		t.Fatalf("expected read-only approvalPolicy to be on-request, got %q", got)
	}
}
