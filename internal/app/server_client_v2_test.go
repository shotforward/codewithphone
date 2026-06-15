package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shotforward/codewithphone/internal/v2/groupsessions"
)

func TestServerClientCreateV2Session(t *testing.T) {
	client := serverClient{
		BaseURL:      "http://server.test",
		MachineID:    "machine_001",
		MachineToken: "token_001",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
				if httpReq.URL.Path != "/v2/sessions" {
					t.Fatalf("path = %q, want /v2/sessions", httpReq.URL.Path)
				}
				if httpReq.Method != http.MethodPost {
					t.Fatalf("method = %q, want POST", httpReq.Method)
				}
				if got := httpReq.Header.Get("X-Machine-Token"); got != "token_001" {
					t.Fatalf("X-Machine-Token = %q", got)
				}
				var req groupsessions.CreateRequest
				if err := json.NewDecoder(httpReq.Body).Decode(&req); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				if req.TemplateID != "codewithphone/sdlc" || len(req.Agents) != 1 {
					t.Fatalf("unexpected request: %+v", req)
				}
				body, err := json.Marshal(v2CreateSessionResponse{
					Session: v2SessionResponse{
						ID:               "sess_001",
						Title:            req.Title,
						TemplateID:       req.TemplateID,
						InteractionModel: req.InteractionModel,
						MachineID:        req.MachineID,
						Runtime:          req.Runtime,
						WorkspaceRoot:    req.WorkspaceRoot,
						Status:           "active",
					},
					AgentSessions: []v2AgentSessionResponse{{
						ID:          "asess_001",
						SessionID:   "sess_001",
						TemplateID:  req.TemplateID,
						AgentID:     "pm",
						DisplayName: "Product Manager",
						Mention:     "@pm",
					}},
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			}),
		},
	}
	result, err := client.createV2Session(context.Background(), groupsessions.CreateRequest{
		Title:            "Build v2",
		MachineID:        "machine_001",
		Runtime:          "codex_cli",
		WorkspaceRoot:    "/workspace/project",
		TemplateID:       "codewithphone/sdlc",
		InteractionModel: "agent_group",
		Agents: []groupsessions.AgentRequest{{
			AgentID:     "pm",
			DisplayName: "Product Manager",
			Mention:     "@pm",
		}},
	})
	if err != nil {
		t.Fatalf("createV2Session() error = %v", err)
	}
	if result.Session.ID != "sess_001" || len(result.AgentSessions) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestServerClientClaimTaskDecodesV2AgentMetadata(t *testing.T) {
	client := serverClient{
		BaseURL:      "http://server.test",
		MachineID:    "machine_001",
		MachineToken: "token_001",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
				if httpReq.URL.Path != "/v1/machines/machine_001/tasks/claim" {
					t.Fatalf("path = %q", httpReq.URL.Path)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"taskRunId": "task_001",
						"sessionId": "sess_001",
						"templateId": "codewithphone/sdlc",
						"agentSessionId": "asess_001",
						"agentId": "pm",
						"agentDisplayName": "Product Manager",
						"agentMention": "@pm",
						"runSegmentId": "run_001",
						"mode": "discuss",
						"runtime": "codex_cli",
						"workspaceRoot": "/workspace/project",
						"prompt": "@pm clarify"
					}`)),
				}, nil
			}),
		},
	}

	dispatch, err := client.claimTask(context.Background())
	if err != nil {
		t.Fatalf("claimTask() error = %v", err)
	}
	if dispatch.TemplateID != "codewithphone/sdlc" ||
		dispatch.AgentSessionID != "asess_001" ||
		dispatch.AgentID != "pm" ||
		dispatch.AgentDisplayName != "Product Manager" ||
		dispatch.AgentMention != "@pm" ||
		dispatch.RunSegmentID != "run_001" ||
		dispatch.Mode != "discuss" {
		t.Fatalf("v2 agent metadata not decoded: %+v", dispatch)
	}
}
