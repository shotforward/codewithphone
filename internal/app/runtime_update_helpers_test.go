package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeNpmRegistry returns an httptest.Server that responds to the npm
// "latest" metadata endpoint with the supplied version. If status is
// non-zero it short-circuits with that HTTP status and an empty body so
// tests can exercise registry-failure paths.
//
// Shared between codex_update_test.go and gemini_update_test.go because
// the npm /latest contract is identical across the two packages and there
// is no value in maintaining two copies of the same handler.
func fakeNpmRegistry(t *testing.T, version string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version})
	}))
}
