package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildClaudeHookSettingsFromScratch(t *testing.T) {
	got := buildClaudeHookSettings(nil, "/usr/local/bin/codewithphone")

	hooks, ok := got["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("expected hooks map, got %T", got["hooks"])
	}

	post := asAnySlice(hooks["PostToolUse"])
	if len(post) != 1 {
		t.Fatalf("expected 1 PostToolUse entry, got %d", len(post))
	}
	entry, _ := post[0].(map[string]any)
	if entry["matcher"] != "Write|Edit|MultiEdit|NotebookEdit" {
		t.Fatalf("unexpected matcher: %v", entry["matcher"])
	}
	cmds := asAnySlice(entry["hooks"])
	if len(cmds) != 1 {
		t.Fatalf("expected 1 hook command, got %d", len(cmds))
	}
	cmd, _ := cmds[0].(map[string]any)
	if cmd["type"] != "command" {
		t.Errorf("expected type=command, got %v", cmd["type"])
	}
	if !strings.Contains(cmd["command"].(string), "hook-claude-file-touched") {
		t.Errorf("PostToolUse command should reference hook-claude-file-touched, got %v", cmd["command"])
	}

	pre := asAnySlice(hooks["PreToolUse"])
	if len(pre) != 1 {
		t.Fatalf("expected 1 PreToolUse entry, got %d", len(pre))
	}
	preEntry, _ := pre[0].(map[string]any)
	preCmds := asAnySlice(preEntry["hooks"])
	preCmd, _ := preCmds[0].(map[string]any)
	if !strings.Contains(preCmd["command"].(string), "hook-claude-preflight") {
		t.Errorf("PreToolUse command should reference hook-claude-preflight, got %v", preCmd["command"])
	}
}

func TestBuildClaudeHookSettingsMergesExisting(t *testing.T) {
	existing := map[string]any{
		"someExistingKey": "preserve me",
		"hooks": map[string]any{
			"Notification": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{"type": "command", "command": "user-notify.sh"},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Read",
					"hooks": []any{
						map[string]any{"type": "command", "command": "user-read-hook.sh"},
					},
				},
			},
		},
	}
	existingBytes, _ := json.Marshal(existing)
	got := buildClaudeHookSettings(existingBytes, "/bin/cwp")

	if got["someExistingKey"] != "preserve me" {
		t.Errorf("non-hooks top-level keys must be preserved, got %v", got["someExistingKey"])
	}
	hooks := got["hooks"].(map[string]any)

	// Existing Notification must survive untouched.
	if notif := asAnySlice(hooks["Notification"]); len(notif) != 1 {
		t.Errorf("Notification entries lost: %v", notif)
	}

	// Existing user PostToolUse + our addition coexist.
	post := asAnySlice(hooks["PostToolUse"])
	if len(post) != 2 {
		t.Fatalf("expected 2 PostToolUse entries after merge, got %d", len(post))
	}
	foundUser := false
	foundOurs := false
	for _, item := range post {
		entry := item.(map[string]any)
		cmds := asAnySlice(entry["hooks"])
		for _, raw := range cmds {
			cmd := raw.(map[string]any)
			cmdStr := cmd["command"].(string)
			if strings.Contains(cmdStr, "user-read-hook.sh") {
				foundUser = true
			}
			if strings.Contains(cmdStr, "hook-claude-file-touched") {
				foundOurs = true
			}
		}
	}
	if !foundUser {
		t.Error("user's existing PostToolUse hook was dropped")
	}
	if !foundOurs {
		t.Error("our PostToolUse hook was not appended")
	}
}

func TestInstallClaudeWorkspaceHooksRoundTrip(t *testing.T) {
	workspace := t.TempDir()

	// Pre-seed an existing settings.local.json so we can verify restoration.
	settingsDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.local.json")
	originalContent := `{"someExisting":"value"}`
	if err := os.WriteFile(settingsPath, []byte(originalContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := installClaudeWorkspaceHooks(workspace)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// During the turn, the file should contain our hooks merged in.
	mid, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read mid-turn settings: %v", err)
	}
	var midSettings map[string]any
	if err := json.Unmarshal(mid, &midSettings); err != nil {
		t.Fatalf("parse mid-turn settings: %v", err)
	}
	if midSettings["someExisting"] != "value" {
		t.Errorf("original keys not preserved during turn, got %+v", midSettings)
	}
	hooks, ok := midSettings["hooks"].(map[string]any)
	if !ok || len(asAnySlice(hooks["PostToolUse"])) == 0 {
		t.Errorf("PostToolUse hook not installed mid-turn, got %+v", midSettings)
	}

	cleanup()

	// After cleanup, the file should be back to its original content byte-for-byte.
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read restored settings: %v", err)
	}
	if string(restored) != originalContent {
		t.Errorf("settings.local.json not restored, got %q want %q", string(restored), originalContent)
	}
}

func TestInstallClaudeWorkspaceHooksRemovesIfAbsent(t *testing.T) {
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".claude", "settings.local.json")

	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: expected settings.local.json to not exist, got %v", err)
	}

	cleanup, err := installClaudeWorkspaceHooks(workspace)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("expected hook file to exist mid-turn: %v", err)
	}

	cleanup()

	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("expected hook file to be removed after cleanup, got err=%v", err)
	}
}
