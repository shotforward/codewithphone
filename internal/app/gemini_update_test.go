package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withGeminiRegistryURL swaps the npm registry endpoint for the duration
// of the test and restores the original on cleanup. Mirror of
// withCodexRegistryURL.
func withGeminiRegistryURL(t *testing.T, url string) {
	t.Helper()
	original := geminiNpmRegistryURL
	geminiNpmRegistryURL = url
	t.Cleanup(func() { geminiNpmRegistryURL = original })
}

func TestCheckGeminiUpdateReportsAvailableWhenNewer(t *testing.T) {
	srv := fakeNpmRegistry(t, "0.50.0", 0)
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	latest, available, command := checkGeminiUpdate(context.Background(), "0.37.0")
	if latest != "0.50.0" {
		t.Fatalf("expected latest=0.50.0, got %q", latest)
	}
	if !available {
		t.Fatal("expected updateAvailable=true")
	}
	if command != geminiUpdateCommand {
		t.Fatalf("expected updateCommand=%q, got %q", geminiUpdateCommand, command)
	}
}

func TestCheckGeminiUpdateNoneWhenUpToDate(t *testing.T) {
	srv := fakeNpmRegistry(t, "0.37.0", 0)
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	latest, available, command := checkGeminiUpdate(context.Background(), "0.37.0")
	if latest != "0.37.0" {
		t.Fatalf("expected latest=0.37.0, got %q", latest)
	}
	if available {
		t.Fatal("expected updateAvailable=false when versions match")
	}
	if command != "" {
		t.Fatalf("expected empty updateCommand, got %q", command)
	}
}

func TestCheckGeminiUpdateNoneWhenAheadOfRegistry(t *testing.T) {
	// Operator has bleeding-edge build; we should not nag them.
	srv := fakeNpmRegistry(t, "0.30.0", 0)
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	_, available, command := checkGeminiUpdate(context.Background(), "0.37.0")
	if available {
		t.Fatal("expected updateAvailable=false when installed > latest")
	}
	if command != "" {
		t.Fatalf("expected empty updateCommand, got %q", command)
	}
}

func TestCheckGeminiUpdateSkipsWhenInstalledMissing(t *testing.T) {
	// Registry must NOT be hit when the installed version is unknown
	// (e.g. version probe failed). Use a server that fails the test if
	// it receives any request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to npm registry: %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	latest, available, command := checkGeminiUpdate(context.Background(), "")
	if latest != "" || available || command != "" {
		t.Fatalf("expected zero update info for empty installed, got latest=%q available=%v command=%q",
			latest, available, command)
	}
}

func TestCheckGeminiUpdateGracefulOnRegistryError(t *testing.T) {
	srv := fakeNpmRegistry(t, "", http.StatusInternalServerError)
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	latest, available, command := checkGeminiUpdate(context.Background(), "0.37.0")
	if latest != "" || available || command != "" {
		t.Fatalf("expected zero update info on registry error, got latest=%q available=%v command=%q",
			latest, available, command)
	}
}

func TestCheckGeminiUpdateGracefulOnMalformedPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	latest, available, command := checkGeminiUpdate(context.Background(), "0.37.0")
	if latest != "" || available || command != "" {
		t.Fatalf("expected zero update info on malformed payload, got latest=%q available=%v command=%q",
			latest, available, command)
	}
}

func TestCheckGeminiUpdateSurfacesLatestEvenWhenInstalledUnparseable(t *testing.T) {
	srv := fakeNpmRegistry(t, "0.50.0", 0)
	defer srv.Close()
	withGeminiRegistryURL(t, srv.URL)

	latest, available, command := checkGeminiUpdate(context.Background(), "completely-not-a-semver")
	if available {
		t.Fatal("expected updateAvailable=false when installed cannot be parsed")
	}
	if command != "" {
		t.Fatalf("expected empty updateCommand, got %q", command)
	}
	if latest != "0.50.0" {
		t.Fatalf("expected latest=0.50.0, got %q", latest)
	}
}
