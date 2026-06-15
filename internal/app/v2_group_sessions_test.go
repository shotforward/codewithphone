package app

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shotforward/codewithphone/internal/config"
)

func TestHandleCreateV2GroupSession(t *testing.T) {
	var capturedPath string
	var capturedToken string
	var capturedBody string

	svc := New(config.Config{
		MachineID:     "machine_001",
		MachineToken:  "token_001",
		HTTPAddr:      "127.0.0.1:0",
		ServerBaseURL: "http://server.test",
		SQLitePath:    filepath.Join(t.TempDir(), "daemon.db"),
	})
	svc.serverClient.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			capturedPath = req.URL.Path
			capturedToken = req.Header.Get("X-Machine-Token")
			body, _ := io.ReadAll(req.Body)
			capturedBody = string(body)
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"session": {
						"id": "sess_001",
						"title": "Build v2",
						"templateId": "codewithphone/sdlc",
						"interactionModel": "agent_group",
						"machineId": "machine_001",
						"runtime": "codex_cli",
						"workspaceRoot": "/workspace/project",
						"status": "active"
					},
					"agentSessions": [
						{
							"agentSessionId": "asess_001",
							"sessionId": "sess_001",
							"templateId": "codewithphone/sdlc",
							"agentId": "pm",
							"displayName": "Product Manager",
							"mention": "@pm"
						}
					]
				}`)),
			}, nil
		}),
	}

	body := `{
		"templateRoot": "../v2/templates/testdata/sdlc",
		"title": "Build v2",
		"workspaceRoot": "/workspace/project"
	}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v2/group-sessions", bytes.NewReader([]byte(body)))
	svc.handleCreateV2GroupSession(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if capturedPath != "/v2/sessions" {
		t.Fatalf("upstream path = %q", capturedPath)
	}
	if capturedToken != "token_001" {
		t.Fatalf("upstream token = %q", capturedToken)
	}
	if !strings.Contains(capturedBody, `"templateId":"codewithphone/sdlc"`) {
		t.Fatalf("upstream body missing template id: %s", capturedBody)
	}
	if strings.Contains(capturedBody, `"interactionModel"`) {
		t.Fatalf("upstream body should let server resolve official template interaction model: %s", capturedBody)
	}
	if strings.Contains(capturedBody, `"agents"`) {
		t.Fatalf("upstream body should let server resolve official template agents: %s", capturedBody)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
