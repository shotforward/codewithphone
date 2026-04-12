package app

import (
	"context"
	"strings"
)

type turnExecutionProfile struct {
	ReadOnly     bool
	TrackChanges bool
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
	if isGreetingOnlyPrompt(normalized) {
		return turnExecutionProfile{ReadOnly: true, TrackChanges: false}
	}

	if containsAny(normalized, "do not modify files", "without modifying files", "read-only", "readonly", "只读", "不要修改文件", "不修改文件") {
		return turnExecutionProfile{ReadOnly: true, TrackChanges: false}
	}

	return turnExecutionProfile{TrackChanges: true}
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

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
