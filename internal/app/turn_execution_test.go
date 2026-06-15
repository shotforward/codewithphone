package app

import (
	"strings"
	"testing"
)

func TestPlanTurnExecutionProjectIntroNotForcedReadOnly(t *testing.T) {
	profile := planTurnExecution("帮我介绍下这个项目")
	if profile.ReadOnly {
		t.Fatal("expected project introduction prompt to avoid forced read-only")
	}
	if !profile.TrackChanges {
		t.Fatal("expected project introduction prompt to keep change tracking enabled")
	}
}

func TestPlanTurnExecutionWritePrompt(t *testing.T) {
	profile := planTurnExecution("请修改 README.md 并新增一段安装说明")
	if profile.ReadOnly {
		t.Fatal("expected edit prompt to allow writes")
	}
	if !profile.TrackChanges {
		t.Fatal("expected edit prompt to track changes")
	}
}

func TestPlanTurnExecutionExplicitReadOnlyEnglish(t *testing.T) {
	profile := planTurnExecution("Explain this repository in read-only mode and do not modify files.")
	if !profile.ReadOnly {
		t.Fatal("expected explicit read-only prompt to be read-only")
	}
	if !profile.ThreadSandboxReadOnly {
		t.Fatal("expected legacy explicit read-only prompt to use a read-only thread sandbox")
	}
	if !profile.AppendReadOnlyInstructions {
		t.Fatal("expected legacy explicit read-only prompt to append read-only instructions")
	}
}

func TestPlanTurnExecutionGreetingPrompt(t *testing.T) {
	profile := planTurnExecution("你好")
	if !profile.ReadOnly {
		t.Fatal("expected greeting prompt to be read-only")
	}
	if profile.TrackChanges {
		t.Fatal("expected greeting prompt to skip change tracking")
	}
}

func TestPlanTurnExecutionGreetingWithTask(t *testing.T) {
	profile := planTurnExecution("你好，帮我修改 README")
	if profile.ReadOnly {
		t.Fatal("expected task prompt to allow writes")
	}
}

func TestPlanTurnExecutionOrchestratorSkipsChangeTracking(t *testing.T) {
	profile := planTurnExecution(`CODEWITHPHONE ORCHESTRATOR

USER MESSAGE:
继续`)
	if !profile.ReadOnly {
		t.Fatal("expected orchestrator prompt to be read-only")
	}
	if profile.TrackChanges {
		t.Fatal("expected orchestrator prompt to skip change tracking")
	}
	if !profile.ThreadSandboxReadOnly {
		t.Fatal("expected orchestrator prompt to use read-only thread sandbox")
	}
	if profile.AppendReadOnlyInstructions {
		t.Fatal("expected orchestrator prompt to avoid appending generic read-only instructions")
	}
}

func TestPlanTurnExecutionUsesLatestUserMessageForReadOnlyDetection(t *testing.T) {
	profile := planTurnExecution(`CODEWITHPHONE AGENT TURN

# RECENT CONTEXT

Use read-only inspection only. Do not modify files or request file changes.

CURRENT USER MESSAGE:
直接改文件，把中文稿写入 MOBILE_PRD.md`)
	if profile.ReadOnly {
		t.Fatal("expected historical read-only context to avoid forcing the latest write request read-only")
	}
	if !profile.TrackChanges {
		t.Fatal("expected latest write request to track changes")
	}
}

func TestPlanTurnExecutionKeepsLatestUserMessageReadOnly(t *testing.T) {
	profile := planTurnExecution(`CODEWITHPHONE AGENT TURN

# RECENT CONTEXT

请修改 README.md

USER MESSAGE:
只读看看，不要修改文件`)
	if !profile.ReadOnly {
		t.Fatal("expected explicit latest read-only request to stay read-only")
	}
	if !profile.ThreadSandboxReadOnly {
		t.Fatal("expected latest explicit read-only request to use a read-only thread sandbox")
	}
}

func TestPlanTurnExecutionForDispatchWorkModeIgnoresReadOnlyText(t *testing.T) {
	profile := planTurnExecutionForDispatch(taskDispatch{
		Mode:   "work",
		Prompt: "开工实现。\n\nPrior context: Use read-only inspection only. Do not modify files.",
	})
	if profile.ReadOnly {
		t.Fatal("expected explicit work mode to override read-only text in context")
	}
	if !profile.TrackChanges {
		t.Fatal("expected explicit work mode to track changes")
	}
}

func TestPlanTurnExecutionForDispatchDiscussModeIgnoresWriteText(t *testing.T) {
	profile := planTurnExecutionForDispatch(taskDispatch{
		Mode:   "discuss",
		Prompt: "请修改 README.md",
	})
	if !profile.ReadOnly {
		t.Fatal("expected explicit discuss mode to be read-only")
	}
	if profile.TrackChanges {
		t.Fatal("expected explicit discuss mode to skip change tracking")
	}
	if profile.ThreadSandboxReadOnly {
		t.Fatal("expected v2 discuss mode to keep the provider thread mode-neutral")
	}
	if profile.AppendReadOnlyInstructions {
		t.Fatal("expected v2 discuss mode to avoid polluting provider thread context with generic read-only instructions")
	}
}

func TestV2DiscussPromptDoesNotAppendGenericReadOnlyInstructions(t *testing.T) {
	profile := planTurnExecutionForDispatch(taskDispatch{
		Mode:   "discuss",
		Prompt: "@architect 先讨论 API 边界",
	})
	prompt := buildTurnPrompt("CODEWITHPHONE AGENT TURN\n\n# DISCUSSION MODE", profile)
	if strings.Contains(prompt, "Use read-only inspection only.") {
		t.Fatalf("v2 discuss prompt should not append generic read-only overview instructions: %s", prompt)
	}
	instructions := developerInstructionsForProfile(profile, taskDispatch{
		Mode:    "discuss",
		AgentID: "architect",
	})
	if strings.Contains(instructions, "This is a read-only repository overview turn.") {
		t.Fatalf("v2 discuss developer instructions should stay mode-neutral: %s", instructions)
	}
}
