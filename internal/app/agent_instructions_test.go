package app

import (
	"strings"
	"testing"
)

func TestAgentRoleInstructionsForSDLCTemplateQA(t *testing.T) {
	got := agentRoleInstructions(taskDispatch{
		TemplateID:       "codewithphone/sdlc",
		AgentID:          "qa",
		AgentDisplayName: "QA",
		AgentMention:     "@qa",
	})
	for _, want := range []string{
		"You are the QA agent",
		"verify behavior",
		"Do not act as Product Manager or Developer",
		"agent_notice",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected QA role instructions to contain %q, got:\n%s", want, got)
		}
	}
}

func TestDeveloperInstructionsIncludeAgentRole(t *testing.T) {
	got := developerInstructionsForProfile(turnExecutionProfile{}, taskDispatch{
		TemplateID: "codewithphone/sdlc",
		AgentID:    "qa",
	})
	for _, want := range []string{
		"Prefer your built-in apply_patch",
		"You are the QA agent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected developer instructions to contain %q, got:\n%s", want, got)
		}
	}
}

func TestAgentRoleInstructionsFallback(t *testing.T) {
	got := agentRoleInstructions(taskDispatch{
		AgentID:          "researcher",
		AgentDisplayName: "Researcher",
	})
	if !strings.Contains(got, "You are the Researcher agent") {
		t.Fatalf("unexpected fallback role instructions:\n%s", got)
	}
}
