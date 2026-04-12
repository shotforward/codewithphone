package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type toolCallRequest struct {
	SessionID string          `json:"session_id"`
	TaskRunID string          `json:"task_run_id"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Service) handleMCPToolCall(w http.ResponseWriter, r *http.Request) {
	var req toolCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.ToolName == "create_file" || req.ToolName == "write_file" || req.ToolName == "pocketcode_write_file" {
		s.handleWriteFileTool(w, r, req)
		return
	}

	if req.ToolName == "run_command" || req.ToolName == "pocketcode_run_command" {
		s.handleRunShellCommandTool(w, r, req)
		return
	}

	http.Error(w, "unsupported tool: "+req.ToolName, http.StatusBadRequest)
}

func (s *Service) handleRunShellCommandTool(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	toolName := req.ToolName
	if toolName == "" {
		toolName = "run_command"
	}
	dispatch := taskDispatch{SessionID: req.SessionID, TaskRunID: req.TaskRunID}
	toolCallID := fmt.Sprintf("tool_%d", time.Now().UnixNano())
	startedAt := time.Now()
	_ = emitToolCallStarted(r.Context(), &s.serverClient, dispatch, toolCallID, toolName, map[string]any{
		"summary": truncateForLog(string(req.Arguments), 180),
	})
	_ = emitTurnPhase(r.Context(), &s.serverClient, dispatch, turnPhaseRunningTools, nil)

	result := s.executeRunCommandTool(r.Context(), req)
	status := "success"
	if resultIsError(result) {
		status = "failed"
	}
	_ = emitToolCallFinished(r.Context(), &s.serverClient, dispatch, toolCallID, toolName, status, time.Since(startedAt), nil)
	_ = emitTurnPhase(r.Context(), &s.serverClient, dispatch, turnPhaseAnalyzing, nil)
	writeJSON(w, http.StatusOK, result)
}

// handleWriteFileTool writes a file to disk without emitting command card events.
// File changes are tracked by the workspace snapshot and appear in the changeset
// card after the turn completes, consistent with how Codex handles file writes
// (auto-accept during turn, keep/discard via changeset).
func (s *Service) handleWriteFileTool(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	toolName := req.ToolName
	if toolName == "" {
		toolName = "create_file"
	}
	dispatch := taskDispatch{SessionID: req.SessionID, TaskRunID: req.TaskRunID}
	toolCallID := fmt.Sprintf("tool_%d", time.Now().UnixNano())
	startedAt := time.Now()
	_ = emitToolCallStarted(r.Context(), &s.serverClient, dispatch, toolCallID, toolName, map[string]any{
		"summary": truncateForLog(string(req.Arguments), 180),
	})
	_ = emitTurnPhase(r.Context(), &s.serverClient, dispatch, turnPhaseEditing, nil)
	result := s.executeWriteFileTool(r.Context(), req)
	status := "success"
	if resultIsError(result) {
		status = "failed"
	}
	_ = emitToolCallFinished(r.Context(), &s.serverClient, dispatch, toolCallID, toolName, status, time.Since(startedAt), nil)
	_ = emitTurnPhase(r.Context(), &s.serverClient, dispatch, turnPhaseAnalyzing, nil)
	writeJSON(w, http.StatusOK, result)
}
