package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/shotforward/codewithphone/internal/v2/groupsessions"
	"github.com/shotforward/codewithphone/internal/v2/templates"
)

type createLocalV2GroupSessionRequest struct {
	TemplateRoot string `json:"templateRoot"`
	Title        string `json:"title"`
	TitleIntent  string `json:"titleIntent"`

	ProjectID     string `json:"projectId"`
	RuntimeTarget string `json:"runtimeTargetId"`
	MachineID     string `json:"machineId"`
	Runtime       string `json:"runtime"`
	Model         string `json:"model"`
	WorkspaceRoot string `json:"workspaceRoot"`
}

func (s *Service) handleCreateV2GroupSession(w http.ResponseWriter, r *http.Request) {
	var req createLocalV2GroupSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	req.TemplateRoot = strings.TrimSpace(req.TemplateRoot)
	req.Title = strings.TrimSpace(req.Title)
	req.TitleIntent = strings.TrimSpace(req.TitleIntent)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.RuntimeTarget = strings.TrimSpace(req.RuntimeTarget)
	req.MachineID = strings.TrimSpace(req.MachineID)
	req.Runtime = strings.TrimSpace(req.Runtime)
	req.Model = strings.TrimSpace(req.Model)
	req.WorkspaceRoot = strings.TrimSpace(req.WorkspaceRoot)

	if req.TemplateRoot == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "templateRoot is required"})
		return
	}
	if req.MachineID == "" {
		req.MachineID = strings.TrimSpace(s.cfg.MachineID)
	}
	if req.Runtime == "" {
		req.Runtime = "codex_cli"
	}
	if req.MachineID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "machineId is required"})
		return
	}
	if req.WorkspaceRoot == "" && req.RuntimeTarget == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspaceRoot is required when runtimeTargetId is not set"})
		return
	}

	tmpl, err := templates.Load(req.TemplateRoot)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	payload, err := groupsessions.BuildCreateRequest(tmpl, groupsessions.CreateInput{
		Title:         req.Title,
		TitleIntent:   req.TitleIntent,
		ProjectID:     req.ProjectID,
		RuntimeTarget: req.RuntimeTarget,
		MachineID:     req.MachineID,
		Runtime:       req.Runtime,
		Model:         req.Model,
		WorkspaceRoot: req.WorkspaceRoot,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	result, err := s.serverClient.createV2Session(r.Context(), payload)
	if err != nil {
		writeV2GroupSessionUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func writeV2GroupSessionUpstreamError(w http.ResponseWriter, err error) {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 {
		writeJSON(w, statusErr.StatusCode, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}
