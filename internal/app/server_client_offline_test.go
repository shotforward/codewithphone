package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerClientMarkMachineOffline(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: got=%s want=%s", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/v1/machines/machine-offline-test/offline" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Machine-Token"); got != "token-offline-test" {
			t.Fatalf("unexpected machine token header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := serverClient{
		BaseURL:      server.URL,
		MachineID:    "machine-offline-test",
		MachineToken: "token-offline-test",
		HTTPClient:   server.Client(),
	}

	if err := client.markMachineOffline(context.Background()); err != nil {
		t.Fatalf("markMachineOffline returned error: %v", err)
	}
}

func TestServerClientMarkMachineOfflineReturnsStatusError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid machine token"}`))
	}))
	defer server.Close()

	client := serverClient{
		BaseURL:      server.URL,
		MachineID:    "machine-offline-test",
		MachineToken: "token-offline-test",
		HTTPClient:   server.Client(),
	}

	err := client.markMachineOffline(context.Background())
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}

	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected httpStatusError, got %T", err)
	}
	if statusErr.Op != "mark machine offline" {
		t.Fatalf("unexpected op: %q", statusErr.Op)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected status code: %d", statusErr.StatusCode)
	}
}
