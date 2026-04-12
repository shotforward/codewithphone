package app

import (
	"github.com/shotforward/codewithphone/internal/changeset"
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"
)

const (
	turnPhaseQueued       = "queued"
	turnPhaseAnalyzing    = "analyzing"
	turnPhaseEditing      = "editing"
	turnPhaseRunningTools = "running_tools"
	turnPhaseTesting      = "testing"
	turnPhaseFinalizing   = "finalizing"
)

func emitTurnPhase(ctx context.Context, server *serverClient, dispatch taskDispatch, phase string, extra map[string]any) error {
	normalized := normalizeTurnPhase(phase)
	if normalized == "" {
		return nil
	}
	payload := copyMap(extra)
	payload["phase"] = normalized
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "turn.phase.changed",
		Payload:   payload,
	})
}

func emitTurnHeartbeat(ctx context.Context, server *serverClient, dispatch taskDispatch, phase string, elapsed time.Duration) error {
	if elapsed < 0 {
		elapsed = 0
	}
	normalized := normalizeTurnPhase(phase)
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "turn.heartbeat",
		Payload: map[string]any{
			"phase":     normalized,
			"elapsedMs": elapsed.Milliseconds(),
		},
	})
}

func emitAssistantStreamStarted(ctx context.Context, server *serverClient, dispatch taskDispatch, itemID string) error {
	payload := map[string]any{}
	if strings.TrimSpace(itemID) != "" {
		payload["itemId"] = strings.TrimSpace(itemID)
	}
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "assistant.stream.started",
		Payload:   payload,
	})
}

func emitAssistantStreamEnded(ctx context.Context, server *serverClient, dispatch taskDispatch, itemID string) error {
	payload := map[string]any{}
	if strings.TrimSpace(itemID) != "" {
		payload["itemId"] = strings.TrimSpace(itemID)
	}
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "assistant.stream.ended",
		Payload:   payload,
	})
}

func emitToolCallStarted(ctx context.Context, server *serverClient, dispatch taskDispatch, toolCallID, toolName string, extra map[string]any) error {
	payload := copyMap(extra)
	payload["toolCallId"] = strings.TrimSpace(toolCallID)
	payload["toolName"] = strings.TrimSpace(toolName)
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "tool.call.started",
		Payload:   payload,
	})
}

func emitToolCallFinished(ctx context.Context, server *serverClient, dispatch taskDispatch, toolCallID, toolName, status string, duration time.Duration, extra map[string]any) error {
	if duration < 0 {
		duration = 0
	}
	payload := copyMap(extra)
	payload["toolCallId"] = strings.TrimSpace(toolCallID)
	payload["toolName"] = strings.TrimSpace(toolName)
	payload["status"] = strings.TrimSpace(status)
	payload["durationMs"] = duration.Milliseconds()
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "tool.call.finished",
		Payload:   payload,
	})
}

// fileTouchedDiffLimits hold the hard caps that protect storage and transport
// from pathological diffs. They are package-level constants so the daemon and
// any helpers stay consistent.
const (
	// maxFileTouchedDiffBytes is the largest diff string we will include in a
	// file.touched payload. Larger diffs are truncated and marked with
	// diffTruncated=true. The cap is intentionally small enough that even if
	// every modified file in a turn is at the limit, the per-turn blob volume
	// stays bounded.
	maxFileTouchedDiffBytes = 256 * 1024 // 256 KB

	// maxRawDiffAcceptBytes is an escape valve: if a runner hands us a diff
	// larger than this (e.g. an exploded base64 patch), we drop the diff
	// field entirely and only emit path/kind. Anything between this and the
	// truncation limit gets truncated.
	maxRawDiffAcceptBytes = 2 * 1024 * 1024 // 2 MB

	// maxFileSizeForDiffComputation is the largest old/new file size for
	// which we are willing to compute a diff inside the daemon (MCP path).
	// Larger files are skipped to avoid pinning CPU on Myers' algorithm.
	maxFileSizeForDiffComputation int64 = 2 * 1024 * 1024 // 2 MB
)

// emitCumulativeFileTouched is the canonical helper for emitting a
// file.touched event. It computes the file's "vs turn snapshot" cumulative
// diff (current workspace content vs the snapshot taken at turn start),
// truncates if necessary, and only emits when the diff is non-empty.
//
// This replaces the earlier model where runners passed in their own
// per-operation diffs. Cumulative diffs let the inline file edit cards in
// the chat reflect the file's CURRENT state-vs-original-state, so a same-
// file edited 3 times in one turn produces 3 inline cards each showing the
// accumulated changes (rather than per-edit deltas).
//
// path can be absolute or workspace-relative; the helper normalizes it.
// Falls back to a no-op (with a warning log) if the snapshot root is not
// available — that means the turn profile didn't enable change tracking.
func emitCumulativeFileTouched(ctx context.Context, server *serverClient, dispatch taskDispatch, path, kind, source, tool string) {
	if dispatch.WorkspaceSnapshotRoot == "" {
		// No snapshot — turn doesn't track changes. Silently skip.
		return
	}
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return
	}
	relPath := cleaned
	if filepath.IsAbs(cleaned) && strings.TrimSpace(dispatch.WorkspaceRoot) != "" {
		if rel, err := filepath.Rel(dispatch.WorkspaceRoot, cleaned); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = filepath.ToSlash(rel)
		}
	} else if !filepath.IsAbs(cleaned) {
		relPath = filepath.ToSlash(filepath.Clean(cleaned))
	}

	normalizedKind := strings.TrimSpace(strings.ToLower(kind))
	switch normalizedKind {
	case "added", "created", "new":
		normalizedKind = "added"
	case "deleted", "removed":
		normalizedKind = "deleted"
	default:
		normalizedKind = "modified"
	}

	rawDiff, err := changeset.BuildFileDiff(dispatch.WorkspaceSnapshotRoot, dispatch.WorkspaceRoot, relPath, normalizedKind)
	if err != nil {
		log.Printf("[FILE.TOUCHED] taskRun=%s changeset.BuildFileDiff failed path=%s err=%v", dispatch.TaskRunID, relPath, err)
		return
	}
	// Skip the emit when nothing actually changed: this happens when the
	// runner edits a file then reverts it, or rewrites it with identical
	// content. We do not want to spam the chat with no-op file cards.
	if !diffHasContent(rawDiff) {
		return
	}

	diff, truncated, accepted := truncateFileTouchedDiff(rawDiff)
	if !accepted {
		log.Printf("[FILE.TOUCHED] taskRun=%s diff dropped path=%s rawBytes=%d (over hard cap)", dispatch.TaskRunID, relPath, len(rawDiff))
		// Still emit a path-only event so the user sees that the file was
		// edited even though we can't show the diff inline.
		if emitErr := emitFileTouched(ctx, server, dispatch, dispatch.WorkspaceRoot, relPath, normalizedKind, source, tool, "", false); emitErr != nil {
			log.Printf("[FILE.TOUCHED] taskRun=%s emit (no diff) failed: %v", dispatch.TaskRunID, emitErr)
		}
		return
	}

	if emitErr := emitFileTouched(ctx, server, dispatch, dispatch.WorkspaceRoot, relPath, normalizedKind, source, tool, diff, truncated); emitErr != nil {
		log.Printf("[FILE.TOUCHED] taskRun=%s emit failed path=%s: %v", dispatch.TaskRunID, relPath, emitErr)
	}
}

// diffHasContent reports whether a unified diff produced by changeset.BuildFileDiff
// contains any actual hunk lines. An "empty diff" only has the header
// (`diff --git ...`) with no `@@` hunks.
func diffHasContent(diff string) bool {
	return strings.Contains(diff, "\n@@") || strings.Contains(diff, "Binary files differ")
}

// truncateFileTouchedDiff returns a diff bounded by the file.touched cap.
// The boolean indicates whether truncation occurred. A diff that exceeds the
// raw-accept limit is rejected outright (returns "", false) so callers can
// emit a diff-less event instead.
func truncateFileTouchedDiff(diff string) (string, bool, bool) {
	if diff == "" {
		return "", false, true
	}
	if len(diff) > maxRawDiffAcceptBytes {
		return "", false, false
	}
	if len(diff) <= maxFileTouchedDiffBytes {
		return diff, false, true
	}
	const marker = "\n... diff truncated by daemon at file.touched cap ...\n"
	cut := maxFileTouchedDiffBytes - len(marker)
	if cut < 0 {
		cut = 0
	}
	return diff[:cut] + marker, true, true
}

// emitFileTouched announces an in-progress file modification observed during a
// turn. This is advisory: the authoritative diff still arrives via
// changeset.generated at turn end. The frontend uses these events to render a
// live "files modified so far" indicator on the turn card.
//
// kind is one of: "added", "modified", "deleted". Empty defaults to "modified".
// source identifies the producer ("codex", "mcp"). path is workspace-relative
// when workspaceRoot is provided and the path falls inside it; otherwise the
// raw path is used as-is. diff is optional; when present it MUST already be
// bounded (callers should run truncateFileTouchedDiff first).
func emitFileTouched(ctx context.Context, server *serverClient, dispatch taskDispatch, workspaceRoot, path, kind, source, tool, diff string, diffTruncated bool) error {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return nil
	}
	rel := cleaned
	if workspaceRoot = strings.TrimSpace(workspaceRoot); workspaceRoot != "" && filepath.IsAbs(cleaned) {
		if r, err := filepath.Rel(workspaceRoot, cleaned); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
	} else if !filepath.IsAbs(cleaned) {
		rel = filepath.ToSlash(filepath.Clean(cleaned))
	}
	normalizedKind := strings.TrimSpace(strings.ToLower(kind))
	switch normalizedKind {
	case "added", "created", "new":
		normalizedKind = "added"
	case "deleted", "removed":
		normalizedKind = "deleted"
	case "":
		normalizedKind = "modified"
	default:
		normalizedKind = "modified"
	}
	payload := map[string]any{
		"path": rel,
		"kind": normalizedKind,
	}
	if source = strings.TrimSpace(source); source != "" {
		payload["source"] = source
	}
	if tool = strings.TrimSpace(tool); tool != "" {
		payload["tool"] = tool
	}
	if diff != "" {
		payload["diff"] = diff
		if diffTruncated {
			payload["diffTruncated"] = true
		}
	}
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "file.touched",
		Payload:   payload,
	})
}

func emitTurnBlocked(ctx context.Context, server *serverClient, dispatch taskDispatch, reason string, extra map[string]any) error {
	payload := copyMap(extra)
	payload["reason"] = strings.TrimSpace(reason)
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "turn.blocked",
		Payload:   payload,
	})
}

func emitTurnUnblocked(ctx context.Context, server *serverClient, dispatch taskDispatch, reason string, extra map[string]any) error {
	payload := copyMap(extra)
	payload["reason"] = strings.TrimSpace(reason)
	return server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "turn.unblocked",
		Payload:   payload,
	})
}

func normalizeTurnPhase(phase string) string {
	switch strings.TrimSpace(strings.ToLower(phase)) {
	case turnPhaseQueued, turnPhaseAnalyzing, turnPhaseEditing, turnPhaseRunningTools, turnPhaseTesting, turnPhaseFinalizing:
		return strings.TrimSpace(strings.ToLower(phase))
	default:
		return ""
	}
}

func copyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
