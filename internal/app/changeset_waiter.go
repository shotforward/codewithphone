package app

import (
	"context"
	"log"

	"github.com/shotforward/codewithphone/internal/changeset"
)

func (s *Service) startChangeSetWaiter(dispatch taskDispatch, snapshot changeset.Snapshot, changeSet *changeset.GeneratedChangeSet) {
	waitCtx, cancel := context.WithTimeout(context.Background(), changeSetWaitTimeout)

	pending := &pendingChangeSet{
		changeSetID: changeSet.ID,
		sessionID:   dispatch.SessionID,
		taskRunID:   dispatch.TaskRunID,
		snapshot:    snapshot,
		cancel:      cancel,
	}

	s.mu.Lock()
	s.pendingSnapshots[dispatch.SessionID] = pending
	s.mu.Unlock()

	go func() {
		defer cancel()

		// Check if we're still the active pending changeset (not auto-kept).
		isStillPending := func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.pendingSnapshots[dispatch.SessionID] == pending
		}

		decision, err := s.changeSets.waitForDecision(waitCtx, changeSet.ID)
		if err != nil {
			// Timeout or cancelled by autoKeepPendingChangeSet.
			// If auto-kept, the caller already sent the event and cleaned up.
			if !isStillPending() {
				return // already handled by autoKeepPendingChangeSet
			}
			// Timeout — we need to auto-keep ourselves.
			log.Printf("changeset %s timed out, auto-keeping", changeSet.ID)
			s.mu.Lock()
			delete(s.pendingSnapshots, dispatch.SessionID)
			s.mu.Unlock()
			_ = emitTurnUnblocked(context.Background(), &s.serverClient, dispatch, "awaiting_changeset_decision", map[string]any{
				"changeSetId": changeSet.ID,
				"autoKept":    true,
			})
			_ = s.serverClient.postEvent(context.Background(), daemonEvent{
				SessionID: dispatch.SessionID,
				TaskRunID: dispatch.TaskRunID,
				EventType: "changeset.kept",
				Payload: map[string]any{
					"changeSetId": changeSet.ID,
					"status":      "kept",
					"autoKept":    true,
				},
			})
			snapshot.Cleanup()
			return
		}

		// User made an explicit decision — remove from pending and apply.
		s.mu.Lock()
		if cur, ok := s.pendingSnapshots[dispatch.SessionID]; ok && cur == pending {
			delete(s.pendingSnapshots, dispatch.SessionID)
		}
		s.mu.Unlock()

		s.applyChangeSetDecision(dispatch, snapshot, changeSet, decision)
		snapshot.Cleanup()
	}()
}

// autoKeepPendingChangeSet resolves any pending changeset from a previous turn
// so the server marks the old task as completed and allows the new task to be claimed.
func (s *Service) autoKeepPendingChangeSet(sessionID string) {
	s.mu.Lock()
	pending, ok := s.pendingSnapshots[sessionID]
	if ok {
		delete(s.pendingSnapshots, sessionID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	log.Printf("auto-keeping pending changeset %s for session %s (new turn starting)", pending.changeSetID, sessionID)

	// Cancel the background waiter goroutine so it doesn't also try to process.
	pending.cancel()

	_ = emitTurnUnblocked(context.Background(), &s.serverClient, taskDispatch{
		SessionID: pending.sessionID,
		TaskRunID: pending.taskRunID,
	}, "awaiting_changeset_decision", map[string]any{
		"changeSetId": pending.changeSetID,
		"autoKept":    true,
	})

	// Immediately notify server so session/task status moves out of waiting_user.
	_ = s.serverClient.postEvent(context.Background(), daemonEvent{
		SessionID: pending.sessionID,
		TaskRunID: pending.taskRunID,
		EventType: "changeset.kept",
		Payload: map[string]any{
			"changeSetId": pending.changeSetID,
			"status":      "kept",
			"autoKept":    true,
		},
	})

	// Clean up the snapshot (changes are kept as-is).
	pending.snapshot.Cleanup()
}

func (s *Service) applyChangeSetDecision(dispatch taskDispatch, snapshot changeset.Snapshot, changeSet *changeset.GeneratedChangeSet, decision changeSetStatus) {
	ctx := context.Background()
	_ = emitTurnUnblocked(ctx, &s.serverClient, dispatch, "awaiting_changeset_decision", map[string]any{
		"changeSetId": changeSet.ID,
	})
	switch decision.Decision {
	case "keep":
		_ = s.serverClient.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "changeset.kept",
			Payload: map[string]any{
				"changeSetId": changeSet.ID,
				"status":      "kept",
			},
		})
	case "rollback":
		if err := snapshot.Restore(dispatch.WorkspaceRoot); err != nil {
			log.Printf("changeset %s rollback failed: %v", changeSet.ID, err)
			return
		}
		_ = s.serverClient.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "changeset.rolled_back",
			Payload: map[string]any{
				"changeSetId": changeSet.ID,
				"status":      "rolled_back",
			},
		})
	case "selective":
		if err := changeset.ApplySelectiveDecision(snapshot, dispatch.WorkspaceRoot, changeSet.Files, decision.FileDecisions); err != nil {
			log.Printf("changeset %s selective apply failed: %v", changeSet.ID, err)
			return
		}
		_ = s.serverClient.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "changeset.selective_applied",
			Payload: map[string]any{
				"changeSetId":    changeSet.ID,
				"status":         "selective_applied",
				"fileDecisions":  decision.FileDecisions,
				"decidedFileCnt": len(decision.FileDecisions),
			},
		})
	default:
		log.Printf("changeset %s unsupported decision: %s", changeSet.ID, decision.Decision)
	}
}
