package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/shotforward/codewithphone/internal/v2/groupsessions"
)

type v2CreateSessionResponse struct {
	Session       v2SessionResponse        `json:"session"`
	AgentSessions []v2AgentSessionResponse `json:"agentSessions"`
}

type v2SessionResponse struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	TemplateID       string `json:"templateId"`
	InteractionModel string `json:"interactionModel"`
	MachineID        string `json:"machineId"`
	Runtime          string `json:"runtime"`
	Model            string `json:"model,omitempty"`
	WorkspaceRoot    string `json:"workspaceRoot"`
	Status           string `json:"status"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

type v2AgentSessionResponse struct {
	ID           string `json:"agentSessionId"`
	SessionID    string `json:"sessionId"`
	TemplateID   string `json:"templateId"`
	AgentID      string `json:"agentId"`
	DisplayName  string `json:"displayName"`
	Mention      string `json:"mention"`
	ArtifactRoot string `json:"artifactRoot,omitempty"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

func (c serverClient) createV2Session(ctx context.Context, payload groupsessions.CreateRequest) (v2CreateSessionResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return v2CreateSessionResponse{}, err
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/v2/sessions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return v2CreateSessionResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return v2CreateSessionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return v2CreateSessionResponse{}, newHTTPStatusError("create v2 session", resp)
	}
	var result v2CreateSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return v2CreateSessionResponse{}, err
	}
	return result, nil
}
