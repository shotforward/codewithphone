package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestGeminiMessageRoleSupportsNestedModelRole(t *testing.T) {
	raw := map[string]any{
		"type": "message",
		"message": map[string]any{
			"role": "model",
			"content": []any{
				map[string]any{"text": "我是 QA 代理。"},
			},
		},
	}
	if !isGeminiAssistantRole(geminiMessageRole(raw)) {
		t.Fatalf("expected nested model role to be treated as assistant")
	}
	if got := extractGeminiAssistantText(raw); got != "我是 QA 代理。" {
		t.Fatalf("unexpected extracted text: %q", got)
	}
}

func TestGeminiAssistantMessageSupportsAgentRoleAndDeltaWithoutRole(t *testing.T) {
	withAgentRole := map[string]any{
		"type":    "message",
		"role":    "agent",
		"content": "我是 QA 代理。",
	}
	if !isGeminiAssistantMessage(withAgentRole) {
		t.Fatalf("expected agent role to be treated as assistant")
	}

	deltaWithoutRole := map[string]any{
		"type":    "message",
		"delta":   true,
		"content": "我是 QA 代理。",
	}
	if !isGeminiAssistantMessage(deltaWithoutRole) {
		t.Fatalf("expected role-less text delta to be treated as assistant")
	}

	user := map[string]any{
		"type":    "message",
		"role":    "user",
		"content": "请说明你的工作。",
	}
	if isGeminiAssistantMessage(user) {
		t.Fatalf("expected user message not to be treated as assistant")
	}
}

func TestDisableGeminiTopicUpdateNarration(t *testing.T) {
	settings := map[string]any{
		"general": map[string]any{
			"topicUpdateNarration": true,
			"theme":                "Default",
		},
	}
	disableGeminiTopicUpdateNarration(settings)

	general, _ := settings["general"].(map[string]any)
	if got := general["topicUpdateNarration"]; got != false {
		t.Fatalf("general.topicUpdateNarration = %v, want false", got)
	}
	if got := general["theme"]; got != "Default" {
		t.Fatalf("general.theme = %v, want preserved", got)
	}
	experimental, _ := settings["experimental"].(map[string]any)
	if got := experimental["topicUpdateNarration"]; got != false {
		t.Fatalf("experimental.topicUpdateNarration = %v, want false", got)
	}
}

func TestSyncGeminiConfigSkipsMemoryAndProjectState(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		fullPath := filepath.Join(src, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	write("settings.json", `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
	write("gemini-credentials.json", `{"credential":"ok"}`)
	write("google_accounts.json", `{"account":"ok"}`)
	write("GEMINI.md", "DeepStep memory must not leak")
	write("projects.json", `{"/root/codes/DeepStep":"deepstep"}`)
	write("trustedFolders.json", `{"/root/codes/DeepStep":"TRUST_FOLDER"}`)
	write("history/workspace-1/chat.json", `{"memory":"old"}`)
	write("tmp/workspace-1/log.json", `{"memory":"old"}`)
	write("policies/old.toml", "old")
	write("nested/settings.json", "{}")
	if err := os.WriteFile(filepath.Join(dst, "GEMINI.md"), []byte("stale memory"), 0o644); err != nil {
		t.Fatalf("write stale GEMINI.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "trustedFolders.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write stale trustedFolders.json: %v", err)
	}

	if err := syncGeminiConfig(src, dst); err != nil {
		t.Fatalf("syncGeminiConfig: %v", err)
	}

	for _, path := range []string{"settings.json", "gemini-credentials.json", "google_accounts.json"} {
		if _, err := os.Stat(filepath.Join(dst, path)); err != nil {
			t.Fatalf("expected %s to be synced: %v", path, err)
		}
	}
	for _, path := range []string{
		"GEMINI.md",
		"projects.json",
		"trustedFolders.json",
		"history",
		"tmp",
		"policies",
		"nested/settings.json",
	} {
		if _, err := os.Stat(filepath.Join(dst, path)); !os.IsNotExist(err) {
			t.Fatalf("expected %s not to be synced, stat err=%v", path, err)
		}
	}
}

func TestGeminiSessionRefExists(t *testing.T) {
	home := t.TempDir()
	chats := filepath.Join(home, ".gemini", "tmp", "workspace-1", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatalf("mkdir chats: %v", err)
	}
	sessionRef := "3e4607d8-e3c2-4e18-9077-41a7808f33ce"
	if err := os.WriteFile(
		filepath.Join(chats, "session-2026-05-24T00-26-3e4607d8.jsonl"),
		[]byte(`{"sessionId":"`+sessionRef+`","kind":"main"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write chat: %v", err)
	}
	if !geminiSessionRefExists(home, sessionRef) {
		t.Fatalf("expected Gemini session ref to exist")
	}
	if geminiSessionRefExists(home, "missing-session") {
		t.Fatalf("expected missing Gemini session ref not to exist")
	}
}

func TestGeminiDirectStreamSmoke(t *testing.T) {
	if os.Getenv("RUN_GEMINI_DIRECT_SMOKE") != "1" {
		t.Skip("set RUN_GEMINI_DIRECT_SMOKE=1 to run direct Gemini CLI smoke test")
	}
	key, err := loadGeminiAPIKeyFromCredentials()
	if err != nil {
		t.Fatalf("load gemini api key: %v", err)
	}
	if key == "" {
		t.Fatalf("gemini api key is empty")
	}
	home := os.Getenv("GEMINI_DIRECT_HOME")
	if home == "" {
		home = t.TempDir()
	}
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatalf("prepare gemini home: %v", err)
	}
	settingsJSON := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"topicUpdateNarration":false},"experimental":{"topicUpdateNarration":false}}`
	if mcpURL := os.Getenv("GEMINI_DIRECT_MCP_URL"); mcpURL != "" {
		settingsJSON = `{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"topicUpdateNarration":false},"experimental":{"topicUpdateNarration":false},"mcpServers":{"pocketcode":{"url":` + strconv.Quote(mcpURL) + `,"trust":true}}}`
	}
	settings := []byte(settingsJSON)
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), settings, 0o644); err != nil {
		t.Fatalf("write gemini settings: %v", err)
	}
	if os.Getenv("GEMINI_DIRECT_COPY_MEMORY") == "1" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("user home: %v", err)
		}
		memory, err := os.ReadFile(filepath.Join(userHome, ".gemini", "GEMINI.md"))
		if err != nil {
			t.Fatalf("read gemini memory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home, ".gemini", "GEMINI.md"), memory, 0o644); err != nil {
			t.Fatalf("write gemini memory: %v", err)
		}
	}

	prompt := "请回答两个字：你好"
	if os.Getenv("GEMINI_DIRECT_AGENT_PROMPT") == "1" {
		prompt = buildGeminiPrompt(taskDispatch{
			Prompt:           "请用一句话说明你的工作是什么，不要修改文件。",
			TemplateID:       "codewithphone/sdlc",
			AgentID:          "qa",
			AgentDisplayName: "QA",
			AgentMention:     "qa",
		}, turnExecutionProfile{})
	}

	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--skip-trust",
	}
	if os.Getenv("GEMINI_DIRECT_YOLO") == "1" {
		args = append(args, "--approval-mode", "yolo", "--sandbox=false")
	} else {
		args = append(args, "--approval-mode", "plan")
	}
	args = append(args, "--model", "gemini-2.5-flash")
	cmd := exec.Command("gemini", args...)
	cmd.Env = append(os.Environ(), "GEMINI_API_KEY="+key, "GEMINI_CLI_HOME="+home)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gemini failed: %v\n%s", err, string(output))
	}
	t.Logf("gemini stream output:\n%s", string(output))
}
