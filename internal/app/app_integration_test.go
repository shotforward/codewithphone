package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/shotforward/codewithphone/internal/app"
	"github.com/shotforward/codewithphone/internal/config"
)

func TestDaemonHealthAndRuntimeState(t *testing.T) {
	t.Helper()

	addr := reserveTCPAddr(t)
	dbPath := filepath.Join(t.TempDir(), "daemon.db")

	cfg := config.Config{
		HTTPAddr:      addr,
		MachineID:     "machine-test-01",
		MachineToken:  "token-test",
		SQLitePath:    dbPath,
		ServerBaseURL: "http://localhost:8080",
		ServerWSURL:   "ws://localhost:8080/ws/daemon",
	}

	svc := app.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	baseURL := "http://" + addr
	waitForHealthy(t, baseURL+"/healthz")

	health := struct {
		Status    string `json:"status"`
		MachineID string `json:"machineId"`
	}{}
	readJSON(t, baseURL+"/healthz", &health)
	if health.Status != "ok" {
		t.Fatalf("unexpected health status: %s", health.Status)
	}
	if health.MachineID != cfg.MachineID {
		t.Fatalf("unexpected machine id: %s", health.MachineID)
	}

	state := struct {
		MachineID     string `json:"machineId"`
		SQLitePath    string `json:"sqlitePath"`
		ServerBaseURL string `json:"serverBaseUrl"`
		ServerWSURL   string `json:"serverWsUrl"`
	}{}
	readJSON(t, baseURL+"/v1/runtime/state", &state)
	if state.MachineID != cfg.MachineID {
		t.Fatalf("unexpected runtime machine id: %s", state.MachineID)
	}
	if state.SQLitePath != cfg.SQLitePath {
		t.Fatalf("unexpected sqlite path: %s", state.SQLitePath)
	}
	if state.ServerBaseURL != cfg.ServerBaseURL {
		t.Fatalf("unexpected server base url: %s", state.ServerBaseURL)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon shutdown timeout")
	}
}

func reserveTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp addr: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitForHealthy(t *testing.T, url string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("service did not become healthy: %s", url)
}

func readJSON(t *testing.T, url string, out any) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status %d for %s: %s", resp.StatusCode, url, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s response: %v", url, err)
	}
}
