package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// installClaudeWorkspaceHooks writes a per-task .claude/settings.local.json
// inside the workspace containing PostToolUse + PreToolUse hooks. The
// PostToolUse hook calls back to the daemon so that native Write/Edit/
// MultiEdit/NotebookEdit tool uses still produce file.touched events
// against the per-turn workspace snapshot. The PreToolUse hook blocks any
// write attempt whose resolved path escapes the workspace.
//
// We use settings.local.json (not settings.json) because Claude's docs
// designate it as the personal, gitignored override slot — the user's own
// settings.json is left untouched.
//
// Any pre-existing settings.local.json content is preserved by merging
// our hook entries into the existing "hooks" section, and the original
// content (or absence) is restored when cleanup() runs.
//
// Returns a no-op cleanup if self-locating the binary fails — better to
// run without hooks than to fail the whole turn.
func installClaudeWorkspaceHooks(workspaceRoot string) (func(), error) {
	noop := func() {}

	selfBin, err := os.Executable()
	if err != nil {
		return noop, fmt.Errorf("locate self executable: %w", err)
	}
	// Resolve symlinks so the path is stable across PATH lookups.
	if resolved, resolveErr := filepath.EvalSymlinks(selfBin); resolveErr == nil {
		selfBin = resolved
	}

	settingsDir := filepath.Join(workspaceRoot, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return noop, fmt.Errorf("mkdir .claude: %w", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.local.json")

	var existingBytes []byte
	existed := true
	if data, readErr := os.ReadFile(settingsPath); readErr == nil {
		existingBytes = data
	} else if os.IsNotExist(readErr) {
		existed = false
	} else {
		return noop, fmt.Errorf("read existing settings.local.json: %w", readErr)
	}

	merged := buildClaudeHookSettings(existingBytes, selfBin)
	newBytes, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return noop, fmt.Errorf("marshal merged settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, newBytes, 0o644); err != nil {
		return noop, fmt.Errorf("write settings.local.json: %w", err)
	}

	cleanup := func() {
		if existed {
			if err := os.WriteFile(settingsPath, existingBytes, 0o644); err != nil {
				log.Printf("[CLAUDE-HOOK] restore settings.local.json failed: %v", err)
			}
			return
		}
		if err := os.Remove(settingsPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLAUDE-HOOK] remove settings.local.json failed: %v", err)
		}
		// Try to remove .claude if we created it and it's empty.
		_ = os.Remove(settingsDir)
	}
	return cleanup, nil
}

// buildClaudeHookSettings merges our PostToolUse + PreToolUse entries into
// any pre-existing settings.local.json content.
func buildClaudeHookSettings(existing []byte, selfBin string) map[string]any {
	root := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &root)
		if root == nil {
			root = map[string]any{}
		}
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	postEntries := asAnySlice(hooks["PostToolUse"])
	postEntries = append(postEntries, map[string]any{
		"matcher": "Write|Edit|MultiEdit|NotebookEdit",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": selfBin + " hook-claude-file-touched",
			},
		},
	})
	hooks["PostToolUse"] = postEntries

	preEntries := asAnySlice(hooks["PreToolUse"])
	preEntries = append(preEntries, map[string]any{
		"matcher": "Write|Edit|MultiEdit|NotebookEdit",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": selfBin + " hook-claude-preflight",
			},
		},
	})
	hooks["PreToolUse"] = preEntries

	root["hooks"] = hooks
	return root
}

func asAnySlice(v any) []any {
	if v == nil {
		return nil
	}
	if slice, ok := v.([]any); ok {
		return slice
	}
	return nil
}
