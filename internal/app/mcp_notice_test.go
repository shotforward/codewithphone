package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPToolsListIncludesAgentNotice(t *testing.T) {
	for _, tool := range mcpToolsList() {
		if tool["name"] == "agent_notice" {
			return
		}
	}
	t.Fatal("mcpToolsList missing agent_notice")
}

func TestParseAgentNoticeToolArgs(t *testing.T) {
	args, err := parseAgentNoticeToolArgs(json.RawMessage(`{"body":"  Heads up  ","title":" Progress ","level":"warn"}`))
	if err != nil {
		t.Fatalf("parseAgentNoticeToolArgs error = %v", err)
	}
	if args.Body != "Heads up" {
		t.Fatalf("Body = %q", args.Body)
	}
	if args.Title != "Progress" {
		t.Fatalf("Title = %q", args.Title)
	}
	if args.Level != "warning" {
		t.Fatalf("Level = %q", args.Level)
	}
}

func TestParseAgentNoticeToolArgsRejectsEmptyBody(t *testing.T) {
	_, err := parseAgentNoticeToolArgs(json.RawMessage(`{"body":"   "}`))
	if err == nil {
		t.Fatal("parseAgentNoticeToolArgs error = nil, want error")
	}
}

func TestParseAgentNoticeToolArgsCapsBody(t *testing.T) {
	raw := `{"body":"` + strings.Repeat("你", maxAgentNoticeBodyRunes+8) + `"}`
	args, err := parseAgentNoticeToolArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parseAgentNoticeToolArgs error = %v", err)
	}
	if got := len([]rune(args.Body)); got != maxAgentNoticeBodyRunes {
		t.Fatalf("body rune length = %d, want %d", got, maxAgentNoticeBodyRunes)
	}
}
