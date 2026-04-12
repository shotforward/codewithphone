package app

import "testing"

func TestThreadSandboxForProfile(t *testing.T) {
	if got := threadSandboxForProfile(turnExecutionProfile{}); got != "danger-full-access" {
		t.Fatalf("expected non-read-only sandbox to be danger-full-access, got %q", got)
	}
	if got := threadSandboxForProfile(turnExecutionProfile{ReadOnly: true}); got != "read-only" {
		t.Fatalf("expected read-only sandbox to be read-only, got %q", got)
	}
}
