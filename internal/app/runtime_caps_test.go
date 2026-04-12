package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shotforward/codewithphone/internal/config"
)

func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", name, err)
	}
	return path
}

func findRuntimeCapability(t *testing.T, caps runtimeCapabilitiesPayload, runtime string) runtimeCapabilityPayload {
	t.Helper()
	for _, cap := range caps.Runtimes {
		if cap.Runtime == runtime {
			return cap
		}
	}
	t.Fatalf("runtime capability not found: %s", runtime)
	return runtimeCapabilityPayload{}
}

// ---------------------------------------------------------------------------
// Codex model probing
// ---------------------------------------------------------------------------

func TestProbeCodexModelsReadsCache(t *testing.T) {

	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cache := codexModelsCache{
		Models: []codexCacheEntry{
			{Slug: "gpt-5.4", Visibility: "list"},
			{Slug: "gpt-5.4-mini", Visibility: "list"},
			{Slug: "gpt-internal-test", Visibility: "hidden"},
		},
	}
	data, _ := json.Marshal(cache)
	if err := os.WriteFile(filepath.Join(codexDir, "models_cache.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	old := resolveHomeDir
	resolveHomeDir = func() (string, error) { return tmpDir, nil }
	defer func() { resolveHomeDir = old }()

	models, discoverable, err := probeCodexModels(context.Background(), config.Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !discoverable {
		t.Fatal("expected discoverable=true")
	}
	if len(models) != 2 || models[0] != "gpt-5.4" || models[1] != "gpt-5.4-mini" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestProbeCodexModelsFallsBackOnMissingCache(t *testing.T) {

	tmpDir := t.TempDir()

	old := resolveHomeDir
	resolveHomeDir = func() (string, error) { return tmpDir, nil }
	defer func() { resolveHomeDir = old }()

	models, discoverable, err := probeCodexModels(context.Background(), config.Config{})
	if err == nil {
		t.Fatal("expected error for missing cache")
	}
	if discoverable {
		t.Fatal("expected discoverable=false")
	}
	if len(models) == 0 {
		t.Fatal("expected fallback models")
	}
	if models[0] != codexFallbackModels[0] {
		t.Fatalf("expected fallback models, got %+v", models)
	}
}

// ---------------------------------------------------------------------------
// Gemini model probing
// ---------------------------------------------------------------------------

func TestProbeGeminiModelsExtractsFromBundle(t *testing.T) {
	t.Parallel()

	// Create a fake Gemini CLI bundle directory with a chunk file
	// that contains model constant assignments.
	tmpDir := t.TempDir()
	bundleDir := filepath.Join(tmpDir, "bundle")
	os.MkdirAll(bundleDir, 0o755)

	chunkContent := `
var DEFAULT_GEMINI_MODEL = "gemini-2.5-pro";
var DEFAULT_GEMINI_FLASH_MODEL = "gemini-2.5-flash";
var PREVIEW_GEMINI_MODEL = "gemini-3-pro-preview";
var PREVIEW_GEMINI_FLASH_MODEL = "gemini-3-flash-preview";
`
	os.WriteFile(filepath.Join(bundleDir, "chunk-TEST1234.js"), []byte(chunkContent), 0o644)

	// Create a "binary" symlink pointing into the bundle dir so
	// extractGeminiModelsFromBundle can resolve the bundle path.
	binPath := filepath.Join(bundleDir, "gemini.js")
	os.WriteFile(binPath, []byte("#!/usr/bin/env node\n"), 0o755)

	models, discoverable, err := probeGeminiModels(context.Background(), config.Config{
		GeminiBin: binPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !discoverable {
		t.Fatal("expected discoverable=true")
	}
	// Should contain the 4 extracted models + 3 aliases (auto, pro, flash)
	if len(models) != 7 {
		t.Fatalf("expected 7 models, got %d: %+v", len(models), models)
	}
	want := map[string]bool{
		"gemini-2.5-pro":       true,
		"gemini-2.5-flash":     true,
		"gemini-3-pro-preview": true,
		"gemini-3-flash-preview": true,
		"auto": true,
		"pro":  true,
		"flash": true,
	}
	for _, m := range models {
		if !want[m] {
			t.Errorf("unexpected model: %q", m)
		}
	}
}

func TestProbeGeminiModelsFallsBackOnMissingBundle(t *testing.T) {
	t.Parallel()

	models, discoverable, err := probeGeminiModels(context.Background(), config.Config{
		GeminiBin: "/nonexistent/gemini",
	})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if discoverable {
		t.Fatal("expected discoverable=false on fallback")
	}
	if len(models) == 0 {
		t.Fatal("expected fallback models")
	}
	if models[0] != geminiFallbackModels[0] {
		t.Fatalf("expected fallback models, got %+v", models)
	}
}

// ---------------------------------------------------------------------------
// Claude model probing
// ---------------------------------------------------------------------------

func TestProbeClaudeModelsReturnsStaticList(t *testing.T) {
	t.Parallel()

	models, discoverable, err := probeClaudeModels(context.Background(), config.Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !discoverable {
		t.Fatal("expected discoverable=true")
	}
	if len(models) != len(claudeKnownModels) {
		t.Fatalf("expected %d models, got %d", len(claudeKnownModels), len(models))
	}
	for i, m := range models {
		if m != claudeKnownModels[i] {
			t.Fatalf("model[%d]: want %q, got %q", i, claudeKnownModels[i], m)
		}
	}
}

// ---------------------------------------------------------------------------
// End-to-end detection
// ---------------------------------------------------------------------------

func TestDetectRuntimeCapabilitiesIntegration(t *testing.T) {

	tmpDir := t.TempDir()

	// Set up fake Gemini binary (version only)
	geminiBin := writeExecutable(t, tmpDir, "gemini-fake", `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then
  echo "gemini-cli 1.2.3"
  exit 0
fi
exit 1
`)

	// Set up fake Claude binary
	claudeBin := writeExecutable(t, tmpDir, "claude-fake", `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then
  echo "claude-code 2.0.0"
  exit 0
fi
exit 1
`)

	// Set up Codex cache
	codexDir := filepath.Join(tmpDir, ".codex")
	os.MkdirAll(codexDir, 0o755)
	cache := codexModelsCache{
		Models: []codexCacheEntry{
			{Slug: "gpt-5.4", Visibility: "list"},
		},
	}
	data, _ := json.Marshal(cache)
	os.WriteFile(filepath.Join(codexDir, "models_cache.json"), data, 0o644)

	oldHome := resolveHomeDir
	resolveHomeDir = func() (string, error) { return tmpDir, nil }
	defer func() { resolveHomeDir = oldHome }()

	// Codex binary missing intentionally – version probe will fail, but model probe reads cache
	caps := detectRuntimeCapabilities(context.Background(), config.Config{
		CodexBin:    filepath.Join(tmpDir, "missing-codex"),
		GeminiBin:   geminiBin,
		GeminiModel: "gemini-3-flash-preview",
		ClaudeBin:   claudeBin,
		ClaudeModel: "sonnet",
	})

	// Codex: models from cache, version probe error
	codex := findRuntimeCapability(t, caps, "codex_cli")
	if len(codex.Models) != 1 || codex.Models[0] != "gpt-5.4" {
		t.Fatalf("unexpected codex models: %+v", codex.Models)
	}
	if codex.CLIVersion != "" {
		t.Fatalf("expected empty codex version (binary missing), got %q", codex.CLIVersion)
	}
	if !codex.Discoverable {
		t.Fatal("expected codex discoverable=true from cache")
	}

	// Gemini: version probe succeeds, but bundle extraction fails (fake binary
	// has no chunk-*.js files), so we get fallback models with discoverable=false.
	gemini := findRuntimeCapability(t, caps, "gemini_cli")
	if gemini.CLIVersion != "gemini-cli 1.2.3" {
		t.Fatalf("unexpected gemini version: %q", gemini.CLIVersion)
	}
	if len(gemini.Models) != len(geminiFallbackModels) {
		t.Fatalf("unexpected gemini models (want %d fallback, got %d): %+v", len(geminiFallbackModels), len(gemini.Models), gemini.Models)
	}

	// Claude: version + static models
	claude := findRuntimeCapability(t, caps, "claude_code_cli")
	if claude.CLIVersion != "claude-code 2.0.0" {
		t.Fatalf("unexpected claude version: %q", claude.CLIVersion)
	}
	if !claude.Discoverable {
		t.Fatal("expected claude discoverable=true")
	}
	if len(claude.Models) != len(claudeKnownModels) {
		t.Fatalf("unexpected claude models: %+v", claude.Models)
	}
}

// ---------------------------------------------------------------------------
// Helpers & classification (unchanged)
// ---------------------------------------------------------------------------

func TestClassifyProbeError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   error
		want string
	}{
		{
			name: "auth required",
			in:   errors.New("Not logged in · Please run /login"),
			want: "auth_required: Not logged in · Please run /login",
		},
		{
			name: "unsupported",
			in:   errors.New("unknown option '--json'"),
			want: "unsupported: unknown option '--json'",
		},
		{
			name: "timeout",
			in:   errors.New("codex models timed out"),
			want: "timeout: codex models timed out",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyProbeError(tc.in)
			if got != tc.want {
				t.Fatalf("classifyProbeError mismatch: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestSanitizeProbeOutputStripsANSI(t *testing.T) {
	t.Parallel()

	raw := "\x1b[31mgemini-2.0-flash\x1b[0m\ngemini-1.5-pro\n"
	got := sanitizeProbeOutput(raw)
	if got != "gemini-2.0-flash\ngemini-1.5-pro" {
		t.Fatalf("unexpected sanitized output: %q", got)
	}
}

func TestLooksLikeModelName(t *testing.T) {
	t.Parallel()

	good := []string{"gpt-5.4", "gemini-2.5-flash", "claude-opus-4-6", "sonnet", "opus", "haiku"}
	for _, name := range good {
		if !looksLikeModelName(name) {
			t.Errorf("expected %q to look like a model name", name)
		}
	}
	bad := []string{"", "-bad", "has spaces here", "x"}
	for _, name := range bad {
		if looksLikeModelName(name) {
			t.Errorf("expected %q to NOT look like a model name", name)
		}
	}
}
