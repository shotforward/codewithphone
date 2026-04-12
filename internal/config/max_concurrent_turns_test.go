package config

import "testing"

func TestNormalizeMaxConcurrentTurns(t *testing.T) {
	if got := normalizeMaxConcurrentTurns(0); got != 1 {
		t.Fatalf("expected 1 for zero input, got %d", got)
	}
	if got := normalizeMaxConcurrentTurns(10); got != 10 {
		t.Fatalf("expected 10 to stay 10, got %d", got)
	}
	if got := normalizeMaxConcurrentTurns(100); got != 32 {
		t.Fatalf("expected upper clamp 32, got %d", got)
	}
}
