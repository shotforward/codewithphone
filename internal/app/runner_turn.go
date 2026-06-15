package app

import (
	"context"
	"strings"
)

type turnExecutionProfile struct {
	ReadOnly                   bool
	TrackChanges               bool
	ThreadSandboxReadOnly      bool
	AppendReadOnlyInstructions bool
	// BeforeComplete is invoked by each runner immediately before it emits
	// the final turn.completed event. This lets the dispatch layer slip in
	// side-effects (notably changeset.BuildChangeSet + changeset.generated emission)
	// so that changeset.generated lands BEFORE turn.completed in the
	// timeline — otherwise the web client sees turn.completed first and
	// post-completion events may be dropped or race with the UI state
	// machine.
	BeforeComplete func(ctx context.Context) error
}

// RunBeforeComplete is a nil-safe helper the runners call right before
// emitting turn.completed. Errors are logged by the caller as desired;
// runners always proceed to emit turn.completed even if the hook fails.
func (p turnExecutionProfile) RunBeforeComplete(ctx context.Context) error {
	if p.BeforeComplete == nil {
		return nil
	}
	return p.BeforeComplete(ctx)
}

func planTurnExecution(prompt string) turnExecutionProfile {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	if normalized == "" {
		return turnExecutionProfile{TrackChanges: true}
	}
	if isCodeWithPhoneOrchestratorPrompt(normalized) {
		return turnExecutionProfile{ReadOnly: true, TrackChanges: false, ThreadSandboxReadOnly: true}
	}
	planningText := strings.ToLower(executionPlanningText(prompt))
	if isGreetingOnlyPrompt(planningText) {
		return turnExecutionProfile{ReadOnly: true, TrackChanges: false, ThreadSandboxReadOnly: true, AppendReadOnlyInstructions: true}
	}

	if containsAny(planningText, "do not modify files", "without modifying files", "read-only", "readonly", "只读", "不要修改文件", "不修改文件") {
		return turnExecutionProfile{ReadOnly: true, TrackChanges: false, ThreadSandboxReadOnly: true, AppendReadOnlyInstructions: true}
	}

	return turnExecutionProfile{TrackChanges: true}
}

func isCodeWithPhoneOrchestratorPrompt(normalizedPrompt string) bool {
	return strings.Contains(normalizedPrompt, "codewithphone orchestrator")
}

func planTurnExecutionForDispatch(dispatch taskDispatch) turnExecutionProfile {
	switch strings.ToLower(strings.TrimSpace(dispatch.Mode)) {
	case "discuss":
		return turnExecutionProfile{ReadOnly: true, TrackChanges: false}
	case "work":
		return turnExecutionProfile{TrackChanges: true}
	default:
		return planTurnExecution(dispatch.Prompt)
	}
}

func isGreetingOnlyPrompt(normalizedPrompt string) bool {
	cleaned := strings.Trim(normalizedPrompt, " \t\r\n,.;:!?，。！？、~`'\"()[]{}<>《》【】")
	switch cleaned {
	case "hi", "hello", "hey", "hey there", "hello there", "你好", "嗨", "在吗", "在么":
		return true
	default:
		return false
	}
}

func executionPlanningText(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	for _, marker := range []string{
		"\nCURRENT USER MESSAGE:\n",
		"\nUSER MESSAGE:\n",
	} {
		if idx := strings.LastIndex(trimmed, marker); idx >= 0 {
			return strings.TrimSpace(trimmed[idx+len(marker):])
		}
	}
	return trimmed
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
