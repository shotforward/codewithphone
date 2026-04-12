package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

type codexRunner struct {
	codexBin  string
	server    *serverClient
	approvals approvalClient
	deltaBuf  *EventBuffer
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexItemNotification struct {
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	Item     map[string]any `json:"item"`
}

type codexCommandApprovalRequest struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	ItemID     string `json:"itemId"`
	ApprovalID string `json:"approvalId"`
	Reason     string `json:"reason"`
	Command    string `json:"command"`
	CWD        string `json:"cwd"`
}

type codexFileApprovalRequest struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Reason   string `json:"reason"`
}

type codexPermissionApprovalRequest struct {
	Permissions map[string]any `json:"permissions"`
}

type codexTurnCompletedNotification struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
}

type codexTextDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type codexTurnState struct {
	completedCommandCount int
	earlyFinalize         bool
	streamOpen            bool
	lastPhase             string
	deniedByFingerprint   map[string]approvalStatus
	deniedByCommandRunID  map[string]string
	commandDenied         bool
}

func newCodexRunner(cfg config.Config, server *serverClient) *codexRunner {
	return &codexRunner{
		codexBin: cfg.CodexBin,
		server:   server,
		approvals: approvalClient{
			BaseURL:      cfg.ServerBaseURL,
			HTTPClient:   server.httpClient(),
			PollInterval: 500 * time.Millisecond,
			MachineID:    server.MachineID,
			MachineToken: server.MachineToken,
		},
	}
}

func (r *codexRunner) RunTurn(ctx context.Context, dispatch taskDispatch, providerSessionRef string, profile turnExecutionProfile) (sessionID string, err error) {
	r.deltaBuf = NewEventBuffer(r.server, dispatch.SessionID, dispatch.TaskRunID)
	defer r.deltaBuf.Close()

	t0 := time.Now()
	rpc, rpcErr := startCodexRPC(ctx, r.codexBin)
	if rpcErr != nil {
		return "", rpcErr
	}
	log.Printf("[TIMING] codex startRPC: %v", time.Since(t0))
	defer rpc.close()
	// On any error path below, attach the captured stderr tail so the
	// dispatch layer can surface it on turn.failed events. Errors that
	// are already *runnerError are left untouched (e.g. from nested helpers).
	defer func() {
		if err == nil {
			return
		}
		var alreadyWrapped *runnerError
		if errors.As(err, &alreadyWrapped) {
			return
		}
		err = wrapRunnerError("codex", err, rpc.stderrTail)
	}()
	state := &codexTurnState{}

	t1 := time.Now()
	if err := rpc.initialize(ctx); err != nil {
		return "", err
	}
	log.Printf("[TIMING] codex initialize: %v", time.Since(t1))

	t2 := time.Now()
	threadID, err := rpc.openThread(ctx, dispatch.WorkspaceRoot, providerSessionRef, profile)
	if err != nil {
		return "", err
	}
	log.Printf("[TIMING] codex openThread: %v", time.Since(t2))

	t3 := time.Now()
	if _, err := rpc.request(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"cwd":      dispatch.WorkspaceRoot,
		"input": []map[string]any{
			{
				"type": "text",
				"text": buildTurnPrompt(dispatch.Prompt, profile),
			},
		},
	}, func(msg codexRPCMessage) (bool, error) {
		return r.handleAsyncMessage(ctx, rpc, dispatch, threadID, profile, state, msg)
	}); err != nil {
		return threadID, err
	}
	log.Printf("[TIMING] codex turn/start completed: %v", time.Since(t3))
	if state.earlyFinalize {
		r.deltaBuf.Flush(ctx)
		r.closeAssistantStream(ctx, dispatch, state, "")
		r.emitPhase(ctx, dispatch, state, turnPhaseFinalizing)
		if err := profile.RunBeforeComplete(ctx); err != nil {
			log.Printf("[CHANGESET] codex runner beforeComplete hook failed: %v", err)
		}
		if err := r.server.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "turn.completed",
			Payload: map[string]any{
				"threadId":         threadID,
				"status":           "completed",
				"completionReason": "read_only_answer_ready",
			},
		}); err != nil {
			return threadID, err
		}
		return threadID, nil
	}

	for {
		msg, err := rpc.readMessage()
		if err != nil {
			return threadID, err
		}
		done, err := r.handleAsyncMessage(ctx, rpc, dispatch, threadID, profile, state, msg)
		if err != nil {
			return threadID, err
		}
		if state.earlyFinalize {
			r.deltaBuf.Flush(ctx)
			r.closeAssistantStream(ctx, dispatch, state, "")
			r.emitPhase(ctx, dispatch, state, turnPhaseFinalizing)
			if err := profile.RunBeforeComplete(ctx); err != nil {
				log.Printf("[CHANGESET] codex runner beforeComplete hook failed: %v", err)
			}
			if err := r.server.postEvent(ctx, daemonEvent{
				SessionID: dispatch.SessionID,
				TaskRunID: dispatch.TaskRunID,
				EventType: "turn.completed",
				Payload: map[string]any{
					"threadId":         threadID,
					"status":           "completed",
					"completionReason": "read_only_answer_ready",
				},
			}); err != nil {
				return threadID, err
			}
			return threadID, nil
		}
		if done {
			return threadID, nil
		}
	}
}

func (r *codexRunner) handleAsyncMessage(ctx context.Context, rpc *codexRPCClient, dispatch taskDispatch, threadID string, profile turnExecutionProfile, state *codexTurnState, msg codexRPCMessage) (bool, error) {
	switch {
	case msg.Method == "turn/started":
		return false, nil

	case msg.Method == "item/agentMessage/delta":
		var payload codexTextDeltaNotification
		if err := json.Unmarshal(msg.Params, &payload); err != nil {
			return false, err
		}
		r.openAssistantStream(ctx, dispatch, state, payload.ItemID)
		r.emitPhase(ctx, dispatch, state, turnPhaseFinalizing)
		r.deltaBuf.Append(ctx, payload.Delta, payload.ItemID)
		return false, nil

	case msg.Method == "item/started":
		return false, r.handleItemStarted(ctx, dispatch, state, msg.Params)

	case msg.Method == "item/completed":
		return false, r.handleItemCompleted(ctx, dispatch, profile, state, msg.Params)

	case msg.Method == "item/Execution/requestApproval":
		return false, r.handleCommandApproval(ctx, dispatch, rpc, msg, profile, state)

	case msg.Method == "item/fileChange/requestApproval":
		if profile.ReadOnly {
			return false, rpc.respond(msg.ID, map[string]any{"decision": "decline"})
		}
		// Prevent policy bypass: once a command was denied in this turn,
		// do not silently accept file changes in the same turn.
		if state.commandDenied {
			return false, rpc.respond(msg.ID, map[string]any{"decision": "decline"})
		}
		return false, rpc.respond(msg.ID, map[string]any{"decision": "acceptForSession"})

	case msg.Method == "item/permissions/requestApproval":
		// Security default: do not session-grant broad permission bundles.
		// Command execution should flow through item/Execution/requestApproval.
		permissions := map[string]any{}
		return false, rpc.respond(msg.ID, map[string]any{
			"permissions": permissions,
			"scope":       "once",
		})

	case msg.Method == "turn/completed":
		var payload codexTurnCompletedNotification
		if err := json.Unmarshal(msg.Params, &payload); err != nil {
			return false, err
		}
		r.deltaBuf.Flush(ctx)
		r.closeAssistantStream(ctx, dispatch, state, "")
		r.emitPhase(ctx, dispatch, state, turnPhaseFinalizing)
		if payload.Turn.Status == "completed" {
			if err := profile.RunBeforeComplete(ctx); err != nil {
				log.Printf("[CHANGESET] codex runner beforeComplete hook failed: %v", err)
			}
			return true, r.server.postEvent(ctx, daemonEvent{
				SessionID: dispatch.SessionID,
				TaskRunID: dispatch.TaskRunID,
				EventType: "turn.completed",
				Payload: map[string]any{
					"threadId": threadID,
					"turnId":   payload.Turn.ID,
					"status":   payload.Turn.Status,
				},
			})
		}
		return true, r.server.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "turn.failed",
			Payload: map[string]any{
				"threadId": threadID,
				"turnId":   payload.Turn.ID,
				"status":   payload.Turn.Status,
				"message":  turnFailureMessage(payload),
			},
		})

	default:
		// Defensive: log unknown methods so future Codex CLI additions are
		// visible in daemon logs instead of being silently dropped.
		log.Printf("[CODEX] unhandled async method=%q (taskRun=%s)", msg.Method, dispatch.TaskRunID)
		return false, nil
	}
}

func (r *codexRunner) handleItemStarted(ctx context.Context, dispatch taskDispatch, state *codexTurnState, raw json.RawMessage) error {
	var payload codexItemNotification
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	switch itemType(payload.Item) {
	case "Execution":
		command := unwrapShellWrapperCommandText(itemString(payload.Item, "command"))
		if isLikelyTestCommand(command) {
			r.emitPhase(ctx, dispatch, state, turnPhaseTesting)
		} else {
			r.emitPhase(ctx, dispatch, state, turnPhaseRunningTools)
		}
		return r.server.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "command.started",
			Payload: map[string]any{
				"commandRunId": itemString(payload.Item, "id"),
				"command":      command,
				"cwd":          itemString(payload.Item, "cwd"),
			},
		})
	case "agentMessage":
		r.openAssistantStream(ctx, dispatch, state, itemString(payload.Item, "id"))
		r.emitPhase(ctx, dispatch, state, turnPhaseFinalizing)
		return nil
	case "fileChange":
		r.emitPhase(ctx, dispatch, state, turnPhaseEditing)
		return nil
	case "reasoning":
		r.emitPhase(ctx, dispatch, state, turnPhaseAnalyzing)
		return nil
	default:
		log.Printf("[CODEX] unhandled item/started type=%q (taskRun=%s)", itemType(payload.Item), dispatch.TaskRunID)
		return nil
	}
}

func (r *codexRunner) handleItemCompleted(ctx context.Context, dispatch taskDispatch, profile turnExecutionProfile, state *codexTurnState, raw json.RawMessage) error {
	var payload codexItemNotification
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	switch itemType(payload.Item) {
	case "agentMessage":
		r.openAssistantStream(ctx, dispatch, state, itemString(payload.Item, "id"))
		itemID := itemString(payload.Item, "id")
		text := itemString(payload.Item, "text")
		// Flush buffered deltas before emitting completed text. This avoids
		// late assistant.delta events appending duplicated trailing text in UI.
		r.deltaBuf.Flush(ctx)
		if err := r.server.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "assistant.message.completed",
			Payload: map[string]any{
				"itemId": chooseNonEmpty(itemID, dispatch.TaskRunID),
				"text":   text,
			},
		}); err != nil {
			return err
		}
		if profile.ReadOnly && shouldEarlyFinalizeReadOnlyTurn(text, state.completedCommandCount) {
			state.earlyFinalize = true
		}
		r.closeAssistantStream(ctx, dispatch, state, itemString(payload.Item, "id"))
		return nil
	case "Execution":
		state.completedCommandCount++
		command := unwrapShellWrapperCommandText(itemString(payload.Item, "command"))
		denyType := strings.TrimSpace(itemString(payload.Item, "denyType"))
		commandRunID := itemString(payload.Item, "id")
		if denyType == "" && state.deniedByCommandRunID != nil && commandRunID != "" {
			denyType = strings.TrimSpace(state.deniedByCommandRunID[commandRunID])
		}
		if err := r.server.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "command.finished",
			Payload: map[string]any{
				"commandRunId":     commandRunID,
				"command":          command,
				"cwd":              itemString(payload.Item, "cwd"),
				"status":           itemString(payload.Item, "status"),
				"aggregatedOutput": itemString(payload.Item, "aggregatedOutput"),
				"exitCode":         payload.Item["exitCode"],
				"durationMs":       payload.Item["durationMs"],
				"denyType":         denyType,
			},
		}); err != nil {
			return err
		}
		r.emitPhase(ctx, dispatch, state, turnPhaseAnalyzing)
		return nil
	case "fileChange":
		// We deliberately ignore the per-operation diff from codex's
		// fileChange item and recompute "vs turn snapshot" instead. This
		// gives the user a CUMULATIVE diff (file's current content vs
		// turn-start state) rather than an incremental delta — see the
		// inline file.touched UX where same-file edits in one turn
		// produce N independent cards, each showing accumulated state.
		for _, entry := range codexFileChangeEntries(payload.Item) {
			if entry.path == "" {
				continue
			}
			emitCumulativeFileTouched(ctx, r.server, dispatch, entry.path, entry.kind, "codex", "fileChange")
		}
		r.emitPhase(ctx, dispatch, state, turnPhaseAnalyzing)
		return nil
	case "reasoning":
		r.emitPhase(ctx, dispatch, state, turnPhaseAnalyzing)
		return nil
	default:
		log.Printf("[CODEX] unhandled item/completed type=%q (taskRun=%s)", itemType(payload.Item), dispatch.TaskRunID)
		return nil
	}
}

type codexFileChangeEntry struct {
	path string
	kind string
	// diff is the unified-diff text already prefixed with a `diff --git`
	// header so the frontend renderer can treat it the same as a
	// changeset.generated entry. Empty string means no usable diff.
	diff string
}

// codexFileChangeEntries flattens a Codex fileChange item's `changes` array
// into (path, kind, diff) triples. The Codex CLI emits items shaped like:
//
//	{ "type": "fileChange", "id": "...", "status": "completed",
//	  "changes": [
//	    { "path": "/abs/path", "kind": {"type": "update", ...},
//	      "diff": "@@ -3 +3,2 @@\n..." }
//	  ] }
//
// Codex's diff is just hunk text — we prepend a `diff --git` header so the
// payload is uniform with what changeset.generated produces.
//
// To remain forward-compatible we also fall back to top-level "path"/"kind"
// fields if the changes array is missing.
func codexFileChangeEntries(item map[string]any) []codexFileChangeEntry {
	var entries []codexFileChangeEntry
	if rawChanges, ok := item["changes"].([]any); ok {
		for _, raw := range rawChanges {
			change, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := strings.TrimSpace(itemString(change, "path"))
			if path == "" {
				path = strings.TrimSpace(itemString(change, "filePath"))
			}
			entries = append(entries, codexFileChangeEntry{
				path: path,
				kind: codexNormalizeChangeKind(change["kind"]),
				diff: codexBuildDiffString(path, itemString(change, "diff")),
			})
		}
	}
	if len(entries) == 0 {
		path := strings.TrimSpace(itemString(item, "path"))
		if path == "" {
			path = strings.TrimSpace(itemString(item, "filePath"))
		}
		if path != "" {
			entries = append(entries, codexFileChangeEntry{
				path: path,
				kind: codexNormalizeChangeKind(item["kind"]),
				diff: codexBuildDiffString(path, itemString(item, "diff")),
			})
		}
	}
	return entries
}

// codexBuildDiffString prefixes a Codex hunk-only diff with the standard
// `diff --git` header so downstream renderers can treat it identically to a
// changeset.generated diff. Returns "" for binary diffs and empty input.
func codexBuildDiffString(absPath, hunk string) string {
	hunk = strings.TrimSpace(hunk)
	if hunk == "" {
		return ""
	}
	if strings.Contains(hunk, "Binary files differ") {
		return ""
	}
	rel := strings.TrimPrefix(absPath, "/")
	if rel == "" {
		rel = "file"
	} else {
		rel = filepath.Base(rel)
	}
	// If Codex ever starts shipping a full diff header itself, leave it.
	if strings.HasPrefix(hunk, "diff --git ") {
		return hunk
	}
	return fmt.Sprintf("diff --git a/%s b/%s\n%s", rel, rel, hunk)
}

// codexNormalizeChangeKind maps Codex's change kind representation onto our
// canonical "added"/"modified"/"deleted" vocabulary. Codex uses either a
// plain string or an object like {"type": "update", "move_path": null}.
func codexNormalizeChangeKind(value any) string {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case map[string]any:
		raw, _ = typed["type"].(string)
	}
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "add", "added", "create", "created", "new":
		return "added"
	case "delete", "deleted", "remove", "removed":
		return "deleted"
	case "update", "modify", "modified", "change", "changed", "":
		return "modified"
	default:
		return "modified"
	}
}

func (r *codexRunner) handleCommandApproval(ctx context.Context, dispatch taskDispatch, rpc *codexRPCClient, msg codexRPCMessage, profile turnExecutionProfile, state *codexTurnState) error {
	// Keep approval client auth in sync with the latest runtime machine token.
	// The daemon may obtain/refresh the token during device binding after runner init.
	if r.server != nil {
		r.approvals.BaseURL = r.server.BaseURL
		r.approvals.HTTPClient = r.server.httpClient()
		r.approvals.MachineID = r.server.MachineID
		r.approvals.MachineToken = r.server.MachineToken
	}

	var req codexCommandApprovalRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return err
	}
	rawCommand := unwrapShellWrapperCommandText(req.Command)
	if rawCommand == "" {
		rawCommand = "unknown command"
	}
	commandRunID := strings.TrimSpace(req.ItemID)
	if commandRunID == "" {
		commandRunID = fmt.Sprintf("cmd_%d", time.Now().UnixNano())
	}
	command := normalizeCommandText(rawCommand, req.CWD, req.Reason)

	decision := "accept"
	declineMessage := ""
	denyType := ""
	if profile.ReadOnly && !allowsCommandForProfile(profile, command) {
		decision = "decline"
		denyType = commandDenyTypePolicy
		declineMessage = "command blocked by read-only policy"
		_ = emitTurnBlocked(ctx, r.server, dispatch, "awaiting_approval", map[string]any{
			"mode":      "read_only_policy",
			"decision":  decision,
			"riskLevel": command.RiskLevel,
		})
	} else if !shouldAutoApprove(command) {
		if denied, ok := state.deniedByFingerprint[command.Fingerprint]; ok {
			decision = "decline"
			declineMessage = deniedMessageFromApproval(denied)
			_ = emitTurnBlocked(ctx, r.server, dispatch, "awaiting_approval", map[string]any{
				"mode":               "denied_feedback",
				"decision":           decision,
				"riskLevel":          command.RiskLevel,
				"decisionReason":     strings.TrimSpace(denied.DecisionReason),
				"commandFingerprint": command.Fingerprint,
			})
		} else {
			actionID := approvalActionIDFromRequest(dispatch.TaskRunID, req.ApprovalID, msg.ID)
			_ = emitTurnBlocked(ctx, r.server, dispatch, "awaiting_approval", map[string]any{
				"mode":             "manual",
				"approvalActionId": actionID,
				"riskLevel":        command.RiskLevel,
			})
			if err := r.server.postEvent(ctx, daemonEvent{
				SessionID: dispatch.SessionID,
				TaskRunID: dispatch.TaskRunID,
				EventType: "command.permission_requested",
				Payload: map[string]any{
					"approvalActionId":   actionID,
					"commandRunId":       commandRunID,
					"executable":         command.Executable,
					"args":               command.Args,
					"cwd":                command.CWD,
					"rawCommand":         rawCommand,
					"reason":             command.Reason,
					"riskLevel":          command.RiskLevel,
					"commandFingerprint": command.Fingerprint,
				},
			}); err != nil {
				decision = "decline"
				declineMessage = "command approval failed: " + err.Error()
			} else {
				status, err := r.approvals.waitForDecision(ctx, actionID)
				if err != nil || strings.TrimSpace(status.Decision) != "approve" {
					denyType = commandDenyTypeFromApproval(&status, err)
					if err == nil && strings.TrimSpace(status.Decision) == "deny" {
						if state.deniedByFingerprint == nil {
							state.deniedByFingerprint = map[string]approvalStatus{}
						}
						fingerprint := strings.TrimSpace(status.CommandFingerprint)
						if fingerprint == "" {
							fingerprint = command.Fingerprint
							status.CommandFingerprint = fingerprint
						}
						state.deniedByFingerprint[fingerprint] = status
						declineMessage = deniedMessageFromApproval(status)
					}
					if err != nil {
						declineMessage = "command approval failed: " + err.Error()
					}
					decision = "decline"
				}
			}
		}
	}

	response := map[string]any{"decision": decision}
	if decision == "decline" && strings.TrimSpace(declineMessage) != "" {
		response["message"] = declineMessage
	}
	if decision == "decline" {
		state.commandDenied = true
		if denyType != "" && commandRunID != "" {
			if state.deniedByCommandRunID == nil {
				state.deniedByCommandRunID = map[string]string{}
			}
			state.deniedByCommandRunID[commandRunID] = denyType
		}
	}
	err := rpc.respond(msg.ID, response)
	_ = emitTurnUnblocked(ctx, r.server, dispatch, "awaiting_approval", map[string]any{
		"decision": decision,
	})
	if decision == "accept" {
		r.emitPhase(ctx, dispatch, state, turnPhaseRunningTools)
	} else {
		r.emitPhase(ctx, dispatch, state, turnPhaseAnalyzing)
	}
	return err
}

func deniedMessageFromApproval(status approvalStatus) string {
	reason := strings.TrimSpace(status.DecisionReason)
	if reason == "" {
		return "command denied by user"
	}
	return "command denied by user: " + reason
}

func itemType(item map[string]any) string {
	value, _ := item["type"].(string)
	return value
}

func itemString(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func (r *codexRunner) emitPhase(ctx context.Context, dispatch taskDispatch, state *codexTurnState, phase string) {
	normalized := normalizeTurnPhase(phase)
	if normalized == "" {
		return
	}
	if state != nil && state.lastPhase == normalized {
		return
	}
	if err := emitTurnPhase(ctx, r.server, dispatch, normalized, nil); err != nil {
		log.Printf("codex emit turn.phase.changed(%s) failed: %v", normalized, err)
		return
	}
	if state != nil {
		state.lastPhase = normalized
	}
}

func (r *codexRunner) openAssistantStream(ctx context.Context, dispatch taskDispatch, state *codexTurnState, itemID string) {
	if state != nil && state.streamOpen {
		return
	}
	if err := emitAssistantStreamStarted(ctx, r.server, dispatch, itemID); err != nil {
		log.Printf("codex emit assistant.stream.started failed: %v", err)
		return
	}
	if state != nil {
		state.streamOpen = true
	}
}

func (r *codexRunner) closeAssistantStream(ctx context.Context, dispatch taskDispatch, state *codexTurnState, itemID string) {
	if state != nil && !state.streamOpen {
		return
	}
	if err := emitAssistantStreamEnded(ctx, r.server, dispatch, itemID); err != nil {
		log.Printf("codex emit assistant.stream.ended failed: %v", err)
		return
	}
	if state != nil {
		state.streamOpen = false
	}
}

func isLikelyTestCommand(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, " go test") ||
		strings.HasPrefix(lower, "go test") ||
		strings.Contains(lower, "pytest") ||
		strings.Contains(lower, "npm test") ||
		strings.Contains(lower, "pnpm test") ||
		strings.Contains(lower, "yarn test") ||
		strings.Contains(lower, "cargo test") ||
		strings.Contains(lower, "vitest") ||
		strings.Contains(lower, "jest")
}

func turnFailureMessage(payload codexTurnCompletedNotification) string {
	if payload.Turn.Error != nil && payload.Turn.Error.Message != "" {
		return payload.Turn.Error.Message
	}
	return "codex turn did not complete successfully"
}

func approvalActionIDFromRequest(taskRunID string, approvalID string, rawID json.RawMessage) string {
	base := strings.TrimSpace(approvalID)
	if base == "" {
		base = strings.TrimSpace(string(rawID))
		base = strings.Trim(base, `"`)
	}
	if base == "" {
		base = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	// Keep path-safe ids while still making them unique per PocketCode task run.
	sum := sha1.Sum([]byte(taskRunID + "\n" + base))
	return fmt.Sprintf("approval_%s_%x", taskRunID, sum[:6])
}

type codexRPCClient struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	scanner    *bufio.Scanner
	nextID     atomic.Int64
	stderrTail *stderrTailBuffer
}

func startCodexRPC(ctx context.Context, codexBin string) (*codexRPCClient, error) {
	cmd := exec.CommandContext(ctx, codexBin, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	stderrTail := newStderrTailBuffer(runnerStderrTailLimit)
	go drainCodexStderr(stderr, stderrTail)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	client := &codexRPCClient{
		cmd:        cmd,
		stdin:      stdin,
		scanner:    scanner,
		stderrTail: stderrTail,
	}
	client.nextID.Store(1)
	return client, nil
}

func (c *codexRPCClient) close() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

func (c *codexRPCClient) initialize(ctx context.Context) error {
	if _, err := c.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "codewithphone",
			"title":   "CodeWithPhone Agent",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}, func(_ codexRPCMessage) (bool, error) {
		return false, nil
	}); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func (c *codexRPCClient) openThread(ctx context.Context, workspaceRoot, providerSessionRef string, profile turnExecutionProfile) (string, error) {
	params := map[string]any{
		"cwd":     workspaceRoot,
		"sandbox": threadSandboxForProfile(profile),
	}
	if instructions := developerInstructionsForProfile(profile); instructions != "" {
		params["developerInstructions"] = instructions
	}

	method := "thread/start"
	if providerSessionRef != "" {
		method = "thread/resume"
		params["threadId"] = providerSessionRef
	}

	result, err := c.request(ctx, method, params, func(_ codexRPCMessage) (bool, error) {
		return false, nil
	})
	if err != nil {
		return "", err
	}

	var decoded struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return "", err
	}
	if decoded.Thread.ID == "" {
		return "", errors.New("codex thread id missing from response")
	}
	return decoded.Thread.ID, nil
}

func threadSandboxForProfile(profile turnExecutionProfile) string {
	if profile.ReadOnly {
		return "read-only"
	}
	return "danger-full-access"
}

func buildTurnPrompt(prompt string, profile turnExecutionProfile) string {
	prompt = strings.TrimSpace(prompt)
	if !profile.ReadOnly {
		return prompt
	}

	extra := strings.Join([]string{
		"Use read-only inspection only.",
		"Do not modify files or request file changes.",
		"Inspect a small representative sample, then answer in Chinese.",
		"Prefer these files first: README.md, PRD.md, ARCHITECTURE.md, BOUNDARIES.md, config.yaml, server/README.md, daemon/README.md, server/internal/app/app.go, daemon/internal/app/app.go.",
		"Stop once you can explain the product goal, the main components, and the current implementation stage.",
		"Avoid tests, caches, build artifacts, .next, node_modules, vendor, and third_party unless strictly necessary.",
		"If you already have enough evidence, answer immediately instead of continuing to inspect more files.",
	}, " ")
	return prompt + "\n\n" + extra
}

func developerInstructionsForProfile(profile turnExecutionProfile) string {
	// Always nudge Codex toward its built-in apply_patch tool for text file
	// changes. This makes the daemon receive a structured `fileChange` item
	// (and emit `file.touched`) instead of a `Execution` item with a
	// shell redirect, which carries no file metadata.
	const fileEditPreference = "Prefer your built-in apply_patch / file editing tool for creating, modifying, or deleting text files. " +
		"Use shell commands only for filesystem operations that apply_patch cannot express " +
		"(mkdir, chmod, mv, rm of directories, binary files, or running build/test tooling)."

	if !profile.ReadOnly {
		return fileEditPreference
	}
	return strings.Join([]string{
		"This is a read-only repository overview turn.",
		"Do not make file changes and do not request file-change approvals.",
		"Use at most 5 shell commands and at most 8 file reads.",
		"Do not produce interim natural-language progress updates like '继续确认中'; use command exploration, then give one final answer.",
		"Prefer top-level product and architecture docs plus the main server/daemon entrypoints.",
		"Once you can explain the project goal, architecture, and current implementation stage, stop and answer immediately.",
		fileEditPreference,
	}, " ")
}

func shouldEarlyFinalizeReadOnlyTurn(text string, completedCommandCount int) bool {
	trimmed := strings.TrimSpace(text)
	if completedCommandCount < 2 || len([]rune(trimmed)) < 180 {
		return false
	}
	return containsAny(strings.ToLower(trimmed), "pocketcode", "server", "daemon", "session", "mysql") ||
		containsAny(trimmed, "项目", "架构", "会话", "任务", "PocketCode", "server", "daemon")
}

func (c *codexRPCClient) request(ctx context.Context, method string, params any, handleAsync func(codexRPCMessage) (bool, error)) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	idRaw := json.RawMessage(strconv.AppendInt(nil, id, 10))

	if err := c.write(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}); err != nil {
		return nil, err
	}

	for {
		msg, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		if len(msg.ID) != 0 && bytes.Equal(bytes.TrimSpace(msg.ID), bytes.TrimSpace(idRaw)) && msg.Method == "" {
			if msg.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
			}
			return msg.Result, nil
		}
		if handleAsync == nil {
			continue
		}
		if _, err := handleAsync(msg); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
}

func (c *codexRPCClient) notify(method string, params any) error {
	return c.write(map[string]any{
		"method": method,
		"params": params,
	})
}

func (c *codexRPCClient) respond(rawID json.RawMessage, result any) error {
	return c.write(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{
		ID:     rawID,
		Result: result,
	})
}

func (c *codexRPCClient) write(payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = c.stdin.Write(encoded)
	return err
}

func (c *codexRPCClient) readMessage() (codexRPCMessage, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return codexRPCMessage{}, err
		}
		return codexRPCMessage{}, io.EOF
	}
	var msg codexRPCMessage
	if err := json.Unmarshal(c.scanner.Bytes(), &msg); err != nil {
		return codexRPCMessage{}, err
	}
	return msg, nil
}

func drainCodexStderr(stderr io.ReadCloser, tail *stderrTailBuffer) {
	defer stderr.Close()
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if tail != nil {
			tail.Add(line)
		}
		log.Printf("codex app-server: %s", line)
	}
}
