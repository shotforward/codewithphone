package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestApprovalClientWaitForDecision(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(approvalStatus{
				ID:     "approval_001",
				Status: "pending",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(approvalStatus{
			ID:                 "approval_001",
			Status:             "approved",
			Decision:           "approve",
			Scope:              "session",
			DecisionReason:     "looks good",
			CommandFingerprint: "fp_123",
		})
	}))
	defer server.Close()

	client := approvalClient{
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		PollInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	status, err := client.waitForDecision(ctx, "approval_001")
	if err != nil {
		t.Fatalf("waitForDecision: %v", err)
	}
	if status.Status != "approved" || status.Scope != "session" {
		t.Fatalf("unexpected approval status: %+v", status)
	}
	if status.DecisionReason != "looks good" || status.CommandFingerprint != "fp_123" {
		t.Fatalf("unexpected approval decision details: %+v", status)
	}
}
