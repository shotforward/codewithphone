package app

import (
	"context"
	"log"
	"errors"
	"sync"
	"time"

	"github.com/shotforward/codewithphone/internal/changeset"
)

func (s *Service) runTaskLoop(ctx context.Context) error {
	workerCount := s.maxConcurrentTurns()
	log.Printf("task loop started with %d workers", workerCount)
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for workerID := 1; workerID <= workerCount; workerID++ {
		go func(id int) {
			defer wg.Done()
			s.runTaskWorker(ctx, id)
		}(workerID)
	}

	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

func (s *Service) runTaskWorker(ctx context.Context, workerID int) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		dispatch, err := s.serverClient.claimTask(ctx)
		if err != nil {
			log.Printf("worker=%d claim task failed: %v", workerID, err)
		} else if dispatch != nil {
			log.Printf("[TIMING] worker=%d task claimed: session=%s taskRun=%s runtime=%s", workerID, dispatch.SessionID, dispatch.TaskRunID, dispatch.Runtime)
			t1 := time.Now()
			if err := s.handleDispatch(ctx, *dispatch); err != nil {
				log.Printf("worker=%d handle dispatch %s failed: %v", workerID, dispatch.TaskRunID, err)
			}
			log.Printf("[TIMING] worker=%d handleDispatch total: %v", workerID, time.Since(t1))
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) maxConcurrentTurns() int {
	if s.cfg.MaxConcurrentTurns <= 0 {
		return 1
	}
	return s.cfg.MaxConcurrentTurns
}

func (s *Service) handleDispatch(ctx context.Context, dispatch taskDispatch) error {
	switch dispatch.Runtime {
	case "codex_cli":
		return s.handleRunnerDispatch(ctx, dispatch, s.codexRunner)
	case "gemini_cli":
		return s.handleRunnerDispatch(ctx, dispatch, s.geminiRunner)
	case "claude_code_cli":
		return s.handleRunnerDispatch(ctx, dispatch, s.claudeRunner)
	default:
		return s.serverClient.postEvent(ctx, daemonEvent{SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "turn.failed",
			Payload: map[string]any{
				"message": "runtime not implemented",
				"runtime": dispatch.Runtime,
			},
		})
	}
}

func (s *Service) handleCodexDispatch(ctx context.Context, dispatch taskDispatch) error {
	return s.handleRunnerDispatch(ctx, dispatch, s.codexRunner)
}

func (s *Service) handleRunnerDispatch(ctx context.Context, dispatch taskDispatch, runner turnRunner) error {
	// Serialize tasks for the same session: wait for any previous task
	// (including a cancelled one being drained) to finish before starting.
	sessionLock := s.getSessionLock(dispatch.SessionID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	dispatchStart := time.Now()
	profile := planTurnExecution(dispatch.Prompt)
	// Gemini session is directory-scoped; pin workspace before any task-scoped
	// state (task workspace mapping, snapshots, and MCP file writes) is created.
	if _, isGemini := runner.(*geminiRunner); isGemini {
		dispatch.WorkspaceRoot = s.pinSessionWorkspace(dispatch.SessionID, dispatch.WorkspaceRoot)
	}
	s.setTaskWorkspace(dispatch.TaskRunID, dispatch.WorkspaceRoot)
	s.setTaskProfile(dispatch.TaskRunID, profile)
	defer s.clearTaskWorkspace(dispatch.TaskRunID)
	defer s.clearTaskProfile(dispatch.TaskRunID)
	defer s.clearTaskDeniedApprovals(dispatch.TaskRunID)

	var phaseMu sync.Mutex
	currentPhase := turnPhaseQueued
	setPhase := func(next string) {
		normalized := normalizeTurnPhase(next)
		if normalized == "" {
			return
		}
		phaseMu.Lock()
		if normalized == currentPhase {
			phaseMu.Unlock()
			return
		}
		currentPhase = normalized
		phaseMu.Unlock()
		if err := emitTurnPhase(ctx, &s.serverClient, dispatch, normalized, nil); err != nil {
			log.Printf("failed to emit turn.phase.changed(%s): %v", normalized, err)
		}
	}
	currentPhaseValue := func() string {
		phaseMu.Lock()
		defer phaseMu.Unlock()
		return currentPhase
	}

	// Auto-keep any pending changeset from a previous turn in this session.
	s.autoKeepPendingChangeSet(dispatch.SessionID)
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	terminated := make(chan struct{})
	go s.watchSessionTermination(taskCtx, dispatch.SessionID, dispatch.TaskRunID, cancel, done, terminated)

	t0 := time.Now()
	if err := s.serverClient.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "turn.started",
		Payload: map[string]any{
			"mode": map[string]any{
				"readOnly":     profile.ReadOnly,
				"trackChanges": profile.TrackChanges,
			},
		},
	}); err != nil {
		return err
	}
	if err := emitTurnPhase(ctx, &s.serverClient, dispatch, currentPhase, nil); err != nil {
		log.Printf("failed to emit turn.phase.changed(%s): %v", currentPhase, err)
	}
	setPhase(turnPhaseAnalyzing)
	log.Printf("[TIMING] postEvent(turn.started): %v", time.Since(t0))

	heartbeatStop := make(chan struct{})
	heartbeatStartedAt := time.Now()
	go func() {
		ticker := time.NewTicker(6 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				heartbeatCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				err := emitTurnHeartbeat(heartbeatCtx, &s.serverClient, dispatch, currentPhaseValue(), time.Since(heartbeatStartedAt))
				cancel()
				if err != nil {
					log.Printf("failed to emit turn.heartbeat: %v", err)
				}
			}
		}
	}()
	defer close(heartbeatStop)

	var (
		snapshot    changeset.Snapshot
		err         error
		generatedCS *changeset.GeneratedChangeSet
		csMu        sync.Mutex
	)
	if profile.TrackChanges {
		t1 := time.Now()
		snapshot, err = changeset.CreateSnapshot(dispatch.WorkspaceRoot)
		log.Printf("[TIMING] changeset.CreateSnapshot: %v", time.Since(t1))
		if err != nil {
			log.Printf("[CHANGESET] taskRun=%s changeset.CreateSnapshot failed: %v (workspaceRoot=%s)", dispatch.TaskRunID, err, dispatch.WorkspaceRoot)
			return err
		}
		log.Printf("[CHANGESET] taskRun=%s snapshot created root=%s (workspaceRoot=%s)", dispatch.TaskRunID, snapshot.Root, dispatch.WorkspaceRoot)
		// Expose the snapshot root to the runner so file.touched emitters
		// can compute "vs turn start" cumulative diffs at each write site.
		dispatch.WorkspaceSnapshotRoot = snapshot.Root
		s.setTaskWorkspaceSnapshot(dispatch.TaskRunID, snapshot.Root)
		defer s.clearTaskWorkspaceSnapshot(dispatch.TaskRunID)
		// NOTE: snapshot.Cleanup is NOT deferred here — ownership transfers
		// to the background goroutine or is cleaned up immediately if no changeset.

		// Install BeforeComplete hook so changeset.BuildChangeSet + changeset.generated
		// are emitted BEFORE the runner's turn.completed — otherwise the
		// web client sees turn.completed first and may drop the changeset.
		profile.BeforeComplete = func(hookCtx context.Context) error {
			setPhase(turnPhaseEditing)
			cs, buildErr := changeset.BuildChangeSet(dispatch.TaskRunID, snapshot, dispatch.WorkspaceRoot)
			if buildErr != nil {
				log.Printf("[CHANGESET] taskRun=%s changeset.BuildChangeSet returned error: %v", dispatch.TaskRunID, buildErr)
				return buildErr
			}
			if cs == nil {
				// Even when the snapshot diff is empty (e.g. runner edited a
				// file then reverted it within the same turn), we still emit
				// a changeset.generated event so the web client has a clear
				// "settle" signal. Without this, any preview card created
				// from earlier file.touched events would be stranded in the
				// "previewing" state forever, with the live header
				// "正在修改文件…" never updating.
				log.Printf("[CHANGESET] taskRun=%s no net changes — emitting empty changeset.generated to settle preview state", dispatch.TaskRunID)
				if postErr := s.serverClient.postEvent(hookCtx, daemonEvent{
					SessionID: dispatch.SessionID,
					TaskRunID: dispatch.TaskRunID,
					EventType: "changeset.generated",
					Payload: map[string]any{
						"changeSetId":      "cs_empty_" + dispatch.TaskRunID,
						"summary":          "",
						"changedFileCount": 0,
						"files":            []any{},
					},
				}); postErr != nil {
					log.Printf("[CHANGESET] taskRun=%s postEvent(empty changeset.generated) FAILED: %v", dispatch.TaskRunID, postErr)
				}
				return nil
			}
			log.Printf("[CHANGESET] taskRun=%s posting changeset.generated id=%s fileCount=%d summary=%q",
				dispatch.TaskRunID, cs.ID, cs.ChangedFileCount, cs.Summary)
			if postErr := s.serverClient.postEvent(hookCtx, daemonEvent{
				SessionID: dispatch.SessionID,
				TaskRunID: dispatch.TaskRunID,
				EventType: "changeset.generated",
				Payload: map[string]any{
					"changeSetId":      cs.ID,
					"summary":          cs.Summary,
					"changedFileCount": cs.ChangedFileCount,
					"files":            cs.Files,
				},
			}); postErr != nil {
				log.Printf("[CHANGESET] taskRun=%s postEvent(changeset.generated) FAILED: %v (server likely returned non-2xx)", dispatch.TaskRunID, postErr)
				return postErr
			}
			log.Printf("[CHANGESET] taskRun=%s postEvent(changeset.generated) ok id=%s", dispatch.TaskRunID, cs.ID)
			csMu.Lock()
			generatedCS = cs
			csMu.Unlock()
			return nil
		}
	}

	providerSessionRef := s.getProviderSession(dispatch.SessionID)
	log.Printf("[TIMING] pre-RunTurn setup: %v", time.Since(dispatchStart))
	t2 := time.Now()
	nextRef, err := runner.RunTurn(taskCtx, dispatch, providerSessionRef, profile)
	log.Printf("[TIMING] RunTurn: %v", time.Since(t2))
	if nextRef != "" {
		s.setProviderSession(dispatch.SessionID, nextRef)
	}
	terminatedRequested := false
	select {
	case <-terminated:
		terminatedRequested = true
	default:
	}
	if terminatedRequested {
		snapshot.Cleanup()
		turnCancelled := daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "turn.cancelled",
			Payload: map[string]any{
				"reason": "user_terminated",
				"title":  "Response stopped",
				"body":   "The assistant stopped responding.",
				"tone":   "warning",
			},
		}
		if err := s.serverClient.postEvent(context.Background(), turnCancelled); err != nil {
			log.Printf("failed to post turn.cancelled event: %v", err)
		}
		return nil
	}
	if err != nil {
		snapshot.Cleanup()
		failedPayload := map[string]any{
			"message": err.Error(),
		}
		// Surface the runner's last stderr lines so the UI can show *why*
		// the turn died without users having to dig through daemon logs.
		var rerr *runnerError
		if errors.As(err, &rerr) && len(rerr.StderrTail) > 0 {
			failedPayload["stderrTail"] = rerr.StderrTail
		}
		_ = s.serverClient.postEvent(ctx, daemonEvent{
			SessionID: dispatch.SessionID,
			TaskRunID: dispatch.TaskRunID,
			EventType: "turn.failed",
			Payload:   failedPayload,
		})
		return err
	}

	if !profile.TrackChanges {
		snapshot.Cleanup()
		return nil
	}

	// changeset.BuildChangeSet + changeset.generated were already emitted by the
	// BeforeComplete hook (runs before turn.completed). Here we only need
	// to react to whether the hook found anything.
	csMu.Lock()
	changeSet := generatedCS
	csMu.Unlock()
	if changeSet == nil {
		snapshot.Cleanup()
		return nil
	}

	if err := emitTurnBlocked(ctx, &s.serverClient, dispatch, "awaiting_changeset_decision", map[string]any{
		"changeSetId": changeSet.ID,
	}); err != nil {
		log.Printf("failed to emit turn.blocked for changeset %s: %v", changeSet.ID, err)
	}

	// Hand off to background goroutine — does NOT block the task loop.
	s.startChangeSetWaiter(dispatch, snapshot, changeSet)
	return nil
}


func (s *Service) watchSessionTermination(ctx context.Context, sessionID, taskRunID string, cancel context.CancelFunc, done <-chan struct{}, terminated chan struct{}) {
	ticker := time.NewTicker(sessionTerminationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, err := s.serverClient.fetchTaskStatus(ctx, taskRunID)
			if err != nil {
				// Fallback to session-level check for backward compatibility.
				sessStatus, sessErr := s.serverClient.fetchSessionStatus(ctx, sessionID)
				if sessErr != nil {
					continue
				}
				if sessStatus == "terminated" {
					cancel()
					close(terminated)
					return
				}
				continue
			}
			if status == "cancelled" {
				cancel()
				close(terminated)
				return
			}
		}
	}
}
