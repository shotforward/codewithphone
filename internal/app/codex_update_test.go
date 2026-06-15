package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withCodexRegistryURL swaps the npm registry endpoint for the duration of
// the test and restores the original on cleanup.
func withCodexRegistryURL(t *testing.T, url string) {
	t.Helper()
	original := codexNpmRegistryURL
	codexNpmRegistryURL = url
	t.Cleanup(func() { codexNpmRegistryURL = original })
}

// fakeNpmRegistry lives in runtime_update_helpers_test.go — shared with
// gemini_update_test.go so the two never drift.

func TestCheckCodexUpdateReportsAvailableWhenNewer(t *testing.T) {
	srv := fakeNpmRegistry(t, "0.120.0", 0)
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	latest, available, command := checkCodexUpdate(context.Background(), "codex-cli 0.118.0")
	if latest != "0.120.0" {
		t.Fatalf("expected latest=0.120.0, got %q", latest)
	}
	if !available {
		t.Fatal("expected updateAvailable=true")
	}
	if command != codexUpdateCommand {
		t.Fatalf("expected updateCommand=%q, got %q", codexUpdateCommand, command)
	}
}

func TestCheckCodexUpdateNoneWhenUpToDate(t *testing.T) {
	srv := fakeNpmRegistry(t, "0.118.0", 0)
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	latest, available, command := checkCodexUpdate(context.Background(), "codex-cli 0.118.0")
	if latest != "0.118.0" {
		t.Fatalf("expected latest=0.118.0, got %q", latest)
	}
	if available {
		t.Fatal("expected updateAvailable=false when versions match")
	}
	if command != "" {
		t.Fatalf("expected empty updateCommand, got %q", command)
	}
}

func TestCheckCodexUpdateNoneWhenAheadOfRegistry(t *testing.T) {
	// Operator has bleeding-edge build; we should not nag them.
	srv := fakeNpmRegistry(t, "0.110.0", 0)
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	_, available, command := checkCodexUpdate(context.Background(), "codex-cli 0.118.0")
	if available {
		t.Fatal("expected updateAvailable=false when installed > latest")
	}
	if command != "" {
		t.Fatalf("expected empty updateCommand, got %q", command)
	}
}

func TestCheckCodexUpdateSkipsWhenInstalledMissing(t *testing.T) {
	// Registry must NOT be hit when the installed version is unknown
	// (e.g. version probe failed). Use a server that fails the test if
	// it receives any request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to npm registry: %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	latest, available, command := checkCodexUpdate(context.Background(), "")
	if latest != "" || available || command != "" {
		t.Fatalf("expected zero update info for empty installed, got latest=%q available=%v command=%q",
			latest, available, command)
	}
}

func TestCheckCodexUpdateGracefulOnRegistryError(t *testing.T) {
	srv := fakeNpmRegistry(t, "", http.StatusInternalServerError)
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	latest, available, command := checkCodexUpdate(context.Background(), "codex-cli 0.118.0")
	if latest != "" || available || command != "" {
		t.Fatalf("expected zero update info on registry error, got latest=%q available=%v command=%q",
			latest, available, command)
	}
}

func TestCheckCodexUpdateGracefulOnMalformedPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	latest, available, command := checkCodexUpdate(context.Background(), "codex-cli 0.118.0")
	if latest != "" || available || command != "" {
		t.Fatalf("expected zero update info on malformed payload, got latest=%q available=%v command=%q",
			latest, available, command)
	}
}

func TestCheckCodexUpdateSurfacesLatestEvenWhenInstalledUnparseable(t *testing.T) {
	// If the installed version output is exotic enough that
	// extractVersionNumber returns "", we still want to expose the latest
	// for telemetry/UI but we must NOT claim an update is available.
	srv := fakeNpmRegistry(t, "0.120.0", 0)
	defer srv.Close()
	withCodexRegistryURL(t, srv.URL)

	latest, available, command := checkCodexUpdate(context.Background(), "completely-not-a-semver")
	if available {
		t.Fatal("expected updateAvailable=false when installed cannot be parsed")
	}
	if command != "" {
		t.Fatalf("expected empty updateCommand, got %q", command)
	}
	// Note: latest will be returned only if the parse helper still
	// produced a non-empty latest string; the function returns
	// (latestRaw, false, "") when installed is unparseable.
	if latest != "0.120.0" {
		t.Fatalf("expected latest=0.120.0, got %q", latest)
	}
}
