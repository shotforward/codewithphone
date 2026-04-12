package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

func TestEnsureMachineBindingTokenOnlyRequiresCredentials(t *testing.T) {
	t.Parallel()

	svc := New(config.Config{
		ServerBaseURL: "http://127.0.0.1:1",
		BindMode:      config.BindModeTokenOnly,
	})

	err := svc.ensureMachineBinding(context.Background(), "host", machineInventory{})
	if err == nil {
		t.Fatal("expected error when bind mode token_only has no credentials")
	}
}

func TestEnsureMachineBindingTokenOnlyWithCredentials(t *testing.T) {
	t.Parallel()

	svc := New(config.Config{
		ServerBaseURL: "http://127.0.0.1:1",
		MachineID:     "machine-token-only",
		MachineToken:  "token-ok",
		BindMode:      config.BindModeTokenOnly,
	})

	if err := svc.ensureMachineBinding(context.Background(), "host", machineInventory{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureMachineBindingAutoValidTokenSkipsDeviceAuth(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	heartbeatCalls := 0
	deviceCodeCalls := 0
	tokenHeaderOK := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/machines/machine-auto/heartbeat":
			heartbeatCalls++
			tokenHeaderOK = r.Header.Get("X-Machine-Token") == "token-ok"
			w.WriteHeader(http.StatusOK)
		case "/v1/auth/device-code":
			deviceCodeCalls++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := New(config.Config{
		ServerBaseURL: srv.URL,
		MachineID:     "machine-auto",
		MachineToken:  "token-ok",
		BindMode:      config.BindModeAuto,
	})

	if err := svc.ensureMachineBinding(context.Background(), "host", machineInventory{AllowedRoots: []string{"/tmp"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if heartbeatCalls != 1 {
		t.Fatalf("unexpected heartbeat calls: got=%d want=1", heartbeatCalls)
	}
	if !tokenHeaderOK {
		t.Fatal("heartbeat request missing expected machine token")
	}
	if deviceCodeCalls != 0 {
		t.Fatalf("unexpected device code calls: got=%d want=0", deviceCodeCalls)
	}
	if svc.serverClient.MachineToken != "token-ok" {
		t.Fatalf("expected token to remain unchanged, got %q", svc.serverClient.MachineToken)
	}
}

func TestEnsureMachineBindingAutoUnauthorizedClearsTokenAndFallsBack(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	deviceCodeCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/machines/machine-rebind/heartbeat":
			w.WriteHeader(http.StatusUnauthorized)
		case "/v1/auth/device-code":
			deviceCodeCalls++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := New(config.Config{
		ServerBaseURL: srv.URL,
		MachineID:     "machine-rebind",
		MachineToken:  "token-stale",
		BindMode:      config.BindModeAuto,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := svc.ensureMachineBinding(ctx, "host", machineInventory{AllowedRoots: []string{"/tmp"}})
	if err == nil {
		t.Fatal("expected context cancellation while waiting for fallback auth retries")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.serverClient.MachineToken != "" {
		t.Fatalf("expected token to be cleared after unauthorized probe, got %q", svc.serverClient.MachineToken)
	}
	mu.Lock()
	defer mu.Unlock()
	if deviceCodeCalls == 0 {
		t.Fatal("expected fallback device auth flow to be attempted")
	}
}

func TestEnsureMachineBindingAutoTransientProbeErrorKeepsToken(t *testing.T) {
	t.Parallel()

	svc := New(config.Config{
		ServerBaseURL: "http://127.0.0.1:1",
		MachineID:     "machine-transient",
		MachineToken:  "token-keep",
		BindMode:      config.BindModeAuto,
	})

	if err := svc.ensureMachineBinding(context.Background(), "host", machineInventory{AllowedRoots: []string{"/tmp"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.serverClient.MachineToken != "token-keep" {
		t.Fatalf("expected token to remain unchanged, got %q", svc.serverClient.MachineToken)
	}
}
