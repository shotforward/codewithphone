package app

import "testing"

func TestCommandDenyTypeFromApproval(t *testing.T) {
	t.Run("system error", func(t *testing.T) {
		got := commandDenyTypeFromApproval(nil, errSentinel{})
		if got != commandDenyTypeSystem {
			t.Fatalf("expected %q, got %q", commandDenyTypeSystem, got)
		}
	})

	t.Run("user denied", func(t *testing.T) {
		status := &approvalStatus{Decision: "deny"}
		got := commandDenyTypeFromApproval(status, nil)
		if got != commandDenyTypeUser {
			t.Fatalf("expected %q, got %q", commandDenyTypeUser, got)
		}
	})

	t.Run("approved", func(t *testing.T) {
		status := &approvalStatus{Decision: "approve"}
		got := commandDenyTypeFromApproval(status, nil)
		if got != "" {
			t.Fatalf("expected empty deny type, got %q", got)
		}
	})
}

type errSentinel struct{}

func (errSentinel) Error() string { return "boom" }
