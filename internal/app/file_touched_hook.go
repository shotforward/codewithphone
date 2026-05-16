package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// fileTouchedHookRequest is the body of POST /internal/file/touched, used by
// the in-process Claude PostToolUse hook bridge to report a native file
// write/edit back to the daemon so a file.touched event can be emitted
// against the per-turn workspace snapshot.
type fileTouchedHookRequest struct {
	SessionID string `json:"session_id"`
	TaskRunID string `json:"task_run_id"`
	ToolName  string `json:"tool_name"`
	FilePath  string `json:"file_path"`
	Operation string `json:"operation"` // optional: write|edit|multiedit
}

func (s *Service) handleFileTouchedHook(w http.ResponseWriter, r *http.Request) {
	var req fileTouchedHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.SessionID = strings.TrimSpace(req.SessionID)
	req.TaskRunID = strings.TrimSpace(req.TaskRunID)
	req.FilePath = strings.TrimSpace(req.FilePath)
	if req.SessionID == "" || req.TaskRunID == "" || req.FilePath == "" {
		http.Error(w, "session_id, task_run_id, file_path required", http.StatusBadRequest)
		return
	}

	workspaceRoot := strings.TrimSpace(s.getTaskWorkspace(req.TaskRunID))
	if workspaceRoot == "" {
		http.Error(w, "task workspace not registered", http.StatusNotFound)
		return
	}
	if !filepath.IsAbs(workspaceRoot) {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	}

	absPath := req.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Clean(filepath.Join(workspaceRoot, absPath))
	} else {
		absPath = filepath.Clean(absPath)
	}

	rel, err := filepath.Rel(workspaceRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Stay quiet on out-of-workspace edits — the PreToolUse hook is the
		// gate; this endpoint only mirrors the edit to the changeset.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	kind := "modified"
	if _, statErr := os.Stat(absPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			kind = "deleted"
		}
	} else if op := strings.ToLower(strings.TrimSpace(req.Operation)); op == "write" || op == "create" {
		// The hook fires AFTER the tool already ran, so the file always
		// exists at this point unless deleted by something else. We trust
		// the operation hint when present.
		kind = "added"
	}

	dispatch := taskDispatch{
		SessionID:             req.SessionID,
		TaskRunID:             req.TaskRunID,
		WorkspaceRoot:         workspaceRoot,
		WorkspaceSnapshotRoot: s.getTaskWorkspaceSnapshot(req.TaskRunID),
	}
	tool := strings.TrimSpace(req.ToolName)
	if tool == "" {
		tool = "Edit"
	}
	emitCumulativeFileTouched(r.Context(), &s.serverClient, dispatch, absPath, kind, "claude_hook", tool)

	w.WriteHeader(http.StatusNoContent)
}

// claudeHookPostToolUseInput is what claude pipes to a PostToolUse hook
// command on stdin. We only care about a few fields.
type claudeHookPostToolUseInput struct {
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
}

// RunHookClaudeFileTouched is the entry point for the
// `codewithphone hook-claude-file-touched` subcommand. Claude's PostToolUse
// hook pipes the tool-use JSON to this process on stdin; we extract the
// file path and POST it back to the daemon's /internal/file/touched.
//
// Required env (inherited from the parent claude process):
//   - POCKETCODE_MCP_DAEMON_URL: where the daemon is listening
//   - POCKETCODE_MCP_SESSION_ID: the PocketCode session id
//   - POCKETCODE_MCP_TASK_RUN_ID: the PocketCode task run id
//
// Hook failures must not block Claude — we always exit 0 even on error,
// since PostToolUse fires after the write has succeeded and we'd only be
// noisy by signaling failure.
func RunHookClaudeFileTouched() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CLAUDE-HOOK] panic: %v", r)
			os.Exit(0)
		}
	}()

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Printf("[CLAUDE-HOOK] read stdin: %v", err)
		os.Exit(0)
	}
	var input claudeHookPostToolUseInput
	if err := json.Unmarshal(body, &input); err != nil {
		log.Printf("[CLAUDE-HOOK] parse stdin: %v", err)
		os.Exit(0)
	}

	filePath, _ := input.ToolInput["file_path"].(string)
	if strings.TrimSpace(filePath) == "" {
		// Some Edit variants use other keys — try notebookPath, etc.
		filePath, _ = input.ToolInput["notebook_path"].(string)
	}
	if strings.TrimSpace(filePath) == "" {
		// Nothing to report.
		os.Exit(0)
	}

	baseURL := strings.TrimSpace(os.Getenv("POCKETCODE_MCP_DAEMON_URL"))
	sessionID := strings.TrimSpace(os.Getenv("POCKETCODE_MCP_SESSION_ID"))
	taskRunID := strings.TrimSpace(os.Getenv("POCKETCODE_MCP_TASK_RUN_ID"))
	if baseURL == "" || sessionID == "" || taskRunID == "" {
		log.Printf("[CLAUDE-HOOK] missing daemon env, skipping (url=%q sess=%q task=%q)",
			baseURL, sessionID, taskRunID)
		os.Exit(0)
	}

	op := "edit"
	if input.ToolName == "Write" {
		op = "write"
	}

	payload, err := json.Marshal(fileTouchedHookRequest{
		SessionID: sessionID,
		TaskRunID: taskRunID,
		ToolName:  input.ToolName,
		FilePath:  filePath,
		Operation: op,
	})
	if err != nil {
		log.Printf("[CLAUDE-HOOK] marshal: %v", err)
		os.Exit(0)
	}

	resp, err := http.Post(baseURL+"/internal/file/touched", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		log.Printf("[CLAUDE-HOOK] POST failed: %v", err)
		os.Exit(0)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		log.Printf("[CLAUDE-HOOK] daemon returned %d: %s", resp.StatusCode, string(bodyText))
	}
	os.Exit(0)
}

// RunHookClaudePreflight is the entry point for the
// `codewithphone hook-claude-preflight` subcommand — a PreToolUse hook that
// blocks Write/Edit/MultiEdit attempts whose resolved path escapes the task
// workspace. Returns exit code 2 with a stderr message to make claude
// refuse the tool call (per the PreToolUse contract).
func RunHookClaudePreflight() {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-preflight: read stdin failed")
		os.Exit(0)
	}
	var input claudeHookPostToolUseInput
	if err := json.Unmarshal(body, &input); err != nil {
		fmt.Fprintln(os.Stderr, "claude-preflight: parse stdin failed")
		os.Exit(0)
	}

	filePath, _ := input.ToolInput["file_path"].(string)
	if strings.TrimSpace(filePath) == "" {
		os.Exit(0)
	}
	workspaceRoot := strings.TrimSpace(os.Getenv("POCKETCODE_TASK_WORKSPACE"))
	if workspaceRoot == "" {
		workspaceRoot = strings.TrimSpace(input.CWD)
	}
	if workspaceRoot == "" {
		os.Exit(0)
	}
	if !filepath.IsAbs(workspaceRoot) {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	}
	target := filePath
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(workspaceRoot, target))
	} else {
		target = filepath.Clean(target)
	}
	rel, err := filepath.Rel(workspaceRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		fmt.Fprintf(os.Stderr, "blocked: %q is outside task workspace %q\n", target, workspaceRoot)
		os.Exit(2)
	}
	os.Exit(0)
}
