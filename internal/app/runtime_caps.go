package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

type runtimeCapabilitiesPayload struct {
	Runtimes []runtimeCapabilityPayload `json:"runtimes"`
}

type runtimeCapabilityPayload struct {
	Runtime      string   `json:"runtime"`
	CLIVersion   string   `json:"cliVersion,omitempty"`
	Models       []string `json:"models,omitempty"`
	DefaultModel string   `json:"defaultModel,omitempty"`
	Discoverable bool     `json:"discoverable"`
	ProbeError   string   `json:"probeError,omitempty"`
	ProbedAt     string   `json:"probedAt,omitempty"`
}

const probeCommandTimeout = 4 * time.Second
const httpProbeTimeout = 5 * time.Second

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\a]*(\a|\x1b\\)|\x1b[@-_]`)

// ---------------------------------------------------------------------------
// Testability hooks – overridden in tests.
// ---------------------------------------------------------------------------

var resolveHomeDir = os.UserHomeDir

var geminiModelsAPIURL = "https://generativelanguage.googleapis.com/v1beta/models"

// ---------------------------------------------------------------------------
// Known model tables & fallbacks
// ---------------------------------------------------------------------------

var claudeKnownModels = []string{
	"opus",
	"sonnet",
	"haiku",
	"claude-opus-4-6",
	"claude-sonnet-4-6",
	"claude-haiku-4-5",
}

var codexFallbackModels = []string{
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
}

var geminiFallbackModels = []string{
	"gemini-3-pro-preview",
	"gemini-3-flash-preview",
	"gemini-3.1-pro-preview",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-2.0-flash",
}

// geminiModelConstantPattern matches JS constant assignments like:
//
//	DEFAULT_GEMINI_MODEL = "gemini-2.5-pro"
//	PREVIEW_GEMINI_FLASH_MODEL = "gemini-3-flash-preview"
var geminiModelConstantPattern = regexp.MustCompile(
	`(?:DEFAULT_GEMINI_(?:MODEL|FLASH_MODEL|FLASH_LITE_MODEL)|` +
		`PREVIEW_GEMINI_(?:MODEL|FLASH_MODEL|3_1_MODEL|3_1_FLASH_LITE_MODEL))\s*=\s*"(gemini-[^"]+)"`,
)

// ---------------------------------------------------------------------------
// Default & normalisation helpers (unchanged)
// ---------------------------------------------------------------------------

func defaultRuntimeCapabilities(cfg config.Config) runtimeCapabilitiesPayload {
	geminiDefault := strings.TrimSpace(cfg.GeminiModel)
	if geminiDefault == "" {
		geminiDefault = "gemini-3-flash-preview"
	}
	claudeDefault := strings.TrimSpace(cfg.ClaudeModel)
	if claudeDefault == "" {
		claudeDefault = "sonnet"
	}
	caps := runtimeCapabilitiesPayload{
		Runtimes: []runtimeCapabilityPayload{
			{
				Runtime:      "codex_cli",
				DefaultModel: strings.TrimSpace(cfg.CodexModel),
				Discoverable: false,
			},
			{
				Runtime:      "gemini_cli",
				DefaultModel: geminiDefault,
				Discoverable: false,
			},
			{
				Runtime:      "claude_code_cli",
				DefaultModel: claudeDefault,
				Discoverable: false,
			},
		},
	}
	return normalizeRuntimeCapabilities(caps)
}

func cloneRuntimeCapabilities(in runtimeCapabilitiesPayload) runtimeCapabilitiesPayload {
	out := runtimeCapabilitiesPayload{
		Runtimes: make([]runtimeCapabilityPayload, 0, len(in.Runtimes)),
	}
	for _, cap := range in.Runtimes {
		cloned := cap
		cloned.Models = append([]string(nil), cap.Models...)
		out.Runtimes = append(out.Runtimes, cloned)
	}
	return out
}

func normalizeRuntimeCapability(cap runtimeCapabilityPayload) runtimeCapabilityPayload {
	cap.Runtime = strings.TrimSpace(cap.Runtime)
	cap.CLIVersion = strings.TrimSpace(cap.CLIVersion)
	cap.DefaultModel = strings.TrimSpace(cap.DefaultModel)
	cap.ProbeError = strings.TrimSpace(cap.ProbeError)
	cap.ProbedAt = strings.TrimSpace(cap.ProbedAt)

	seen := map[string]struct{}{}
	models := make([]string, 0, len(cap.Models))
	for _, model := range cap.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 && cap.DefaultModel != "" {
		models = []string{cap.DefaultModel}
	}
	if cap.DefaultModel == "" && len(models) > 0 {
		cap.DefaultModel = models[0]
	}
	if len(models) == 0 {
		cap.Discoverable = false
	}
	cap.Models = models
	return cap
}

func normalizeRuntimeCapabilities(in runtimeCapabilitiesPayload) runtimeCapabilitiesPayload {
	out := runtimeCapabilitiesPayload{
		Runtimes: make([]runtimeCapabilityPayload, 0, len(in.Runtimes)),
	}
	for _, cap := range in.Runtimes {
		cap = normalizeRuntimeCapability(cap)
		if cap.Runtime == "" {
			continue
		}
		out.Runtimes = append(out.Runtimes, cap)
	}
	return out
}

func appendProbeError(current, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return next
	}
	return current + " | " + next
}

func runtimeBinFor(cfg config.Config, runtime string) string {
	switch runtime {
	case "codex_cli":
		return strings.TrimSpace(cfg.CodexBin)
	case "gemini_cli":
		return strings.TrimSpace(cfg.GeminiBin)
	case "claude_code_cli":
		return strings.TrimSpace(cfg.ClaudeBin)
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// CLI version probe (unchanged)
// ---------------------------------------------------------------------------

func sanitizeProbeOutput(raw string) string {
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = ansiEscapePattern.ReplaceAllString(raw, "")
	return strings.TrimSpace(raw)
}

func runCLIProbe(parent context.Context, bin string, env []string, args ...string) (string, error) {
	if strings.TrimSpace(bin) == "" {
		return "", errors.New("cli binary is empty")
	}
	probeCtx, cancel := context.WithTimeout(parent, probeCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, bin, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	text := sanitizeProbeOutput(string(output))
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return text, fmt.Errorf("%s %s timed out", bin, strings.Join(args, " "))
	}
	if err != nil {
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

func firstNonEmptyLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func probeCLIVersion(ctx context.Context, bin string, env []string) (string, error) {
	output, err := runCLIProbe(ctx, bin, env, "--version")
	if err != nil {
		return "", err
	}
	line := firstNonEmptyLine(output)
	if line == "" {
		return "", errors.New("empty version output")
	}
	return line, nil
}

// ---------------------------------------------------------------------------
// Error classification (unchanged)
// ---------------------------------------------------------------------------

func classifyProbeError(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "please run /login"),
		strings.Contains(lower, "manual authorization is required"),
		strings.Contains(lower, "please set an auth method"),
		strings.Contains(lower, "api_key"),
		strings.Contains(lower, "authentication"):
		return "auth_required: " + raw
	case strings.Contains(lower, "unknown option"),
		strings.Contains(lower, "unknown argument"),
		strings.Contains(lower, "no model probe configured"),
		strings.Contains(lower, "not supported"):
		return "unsupported: " + raw
	case strings.Contains(lower, "timed out"):
		return "timeout: " + raw
	default:
		return raw
	}
}

func looksLikeModelName(value string) bool {
	if len(value) < 2 || len(value) > 128 {
		return false
	}
	if strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '/' || r == ':' {
			continue
		}
		return false
	}
	lower := strings.ToLower(value)
	if lower == "sonnet" || lower == "opus" || lower == "haiku" {
		return true
	}
	return strings.ContainsAny(value, "-/.")
}

func dedupeModelNames(models []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || !looksLikeModelName(model) {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func readJSONFile(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func homeDir() string {
	d, _ := resolveHomeDir()
	return d
}

// ---------------------------------------------------------------------------
// Strategy: Codex – read ~/.codex/models_cache.json
// ---------------------------------------------------------------------------

type codexModelsCache struct {
	Models []codexCacheEntry `json:"models"`
}

type codexCacheEntry struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

func probeCodexModels(_ context.Context, _ config.Config) ([]string, bool, error) {
	home := homeDir()
	if home == "" {
		return codexFallbackModels, false, errors.New("cannot resolve home directory")
	}
	cachePath := filepath.Join(home, ".codex", "models_cache.json")

	var cache codexModelsCache
	if err := readJSONFile(cachePath, &cache); err != nil {
		return codexFallbackModels, false, fmt.Errorf("read codex cache: %w", err)
	}

	var models []string
	for _, entry := range cache.Models {
		slug := strings.TrimSpace(entry.Slug)
		if slug == "" {
			continue
		}
		if entry.Visibility == "list" {
			models = append(models, slug)
		}
	}
	models = dedupeModelNames(models)
	if len(models) == 0 {
		return codexFallbackModels, false, errors.New("codex cache contains no listable models")
	}
	return models, true, nil
}

// ---------------------------------------------------------------------------
// Strategy: Gemini – static known models
// ---------------------------------------------------------------------------
//
// The Gemini CLI authenticates via OAuth with cloud-platform scope, which does
// NOT grant access to the generativelanguage.googleapis.com ListModels API
// (that API requires an API key or the generative-language scope, which the
// CLI's OAuth client does not have registered). The CLI also has no "list
// models" subcommand. Therefore we use a static list derived from the models
// present in the Gemini CLI source bundle. This is the same strategy we use
// for Claude.

func probeGeminiModels(_ context.Context, cfg config.Config) ([]string, bool, error) {
	models, err := extractGeminiModelsFromBundle(cfg.GeminiBin)
	if err != nil {
		return geminiFallbackModels, false, fmt.Errorf("gemini bundle probe: %w (using fallback)", err)
	}
	if len(models) == 0 {
		return geminiFallbackModels, false, errors.New("no models extracted from gemini bundle (using fallback)")
	}
	return models, true, nil
}

// extractGeminiModelsFromBundle resolves the Gemini CLI binary to its JS
// bundle directory, scans chunk-*.js files for model constant assignments,
// and returns the unique model IDs found.
func extractGeminiModelsFromBundle(geminiBin string) ([]string, error) {
	bin := strings.TrimSpace(geminiBin)
	if bin == "" {
		return nil, errors.New("gemini binary path is empty")
	}
	realPath, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return nil, fmt.Errorf("resolve gemini binary: %w", err)
	}
	bundleDir := filepath.Dir(realPath)

	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("read bundle dir: %w", err)
	}

	seen := map[string]struct{}{}
	var models []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "chunk-") || !strings.HasSuffix(name, ".js") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(bundleDir, name))
		if err != nil {
			continue
		}
		for _, match := range geminiModelConstantPattern.FindAllSubmatch(data, -1) {
			model := string(match[1])
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
		}
		if len(models) > 0 {
			// All constants live in the same chunk; stop scanning once found.
			break
		}
	}
	if len(models) == 0 {
		return nil, errors.New("no model constants found in bundle chunks")
	}
	// Also include the aliases that Gemini CLI accepts.
	for _, alias := range []string{"auto", "pro", "flash"} {
		if _, ok := seen[alias]; !ok {
			models = append(models, alias)
		}
	}
	return models, nil
}

// ---------------------------------------------------------------------------
// Strategy: Claude – static known models
// ---------------------------------------------------------------------------

func probeClaudeModels(_ context.Context, _ config.Config) ([]string, bool, error) {
	// Claude CLI accepts both aliases (opus, sonnet, haiku) and full model IDs.
	// The alias table is stable and well-known; returning a static list is the
	// most reliable approach since Claude Code has no "list models" command.
	return claudeKnownModels, true, nil
}

// ---------------------------------------------------------------------------
// Top-level detection orchestration
// ---------------------------------------------------------------------------

func detectRuntimeCapabilities(ctx context.Context, cfg config.Config) runtimeCapabilitiesPayload {
	base := defaultRuntimeCapabilities(cfg)
	now := time.Now().UTC().Format(time.RFC3339)
	out := runtimeCapabilitiesPayload{
		Runtimes: make([]runtimeCapabilityPayload, 0, len(base.Runtimes)),
	}

	for _, cap := range base.Runtimes {
		cap.ProbedAt = now
		bin := runtimeBinFor(cfg, cap.Runtime)
		env := os.Environ()

		// Version probe (same for all runtimes)
		if version, err := probeCLIVersion(ctx, bin, env); err != nil {
			cap.ProbeError = appendProbeError(cap.ProbeError, fmt.Sprintf("version probe failed: %v", err))
		} else {
			cap.CLIVersion = version
		}

		// Model discovery (per-runtime strategy)
		var models []string
		var discoverable bool
		var err error
		switch cap.Runtime {
		case "codex_cli":
			models, discoverable, err = probeCodexModels(ctx, cfg)
		case "gemini_cli":
			models, discoverable, err = probeGeminiModels(ctx, cfg)
		case "claude_code_cli":
			models, discoverable, err = probeClaudeModels(ctx, cfg)
		default:
			err = fmt.Errorf("unknown runtime %s", cap.Runtime)
		}

		cap.Models = models
		cap.Discoverable = discoverable
		if err != nil {
			cap.ProbeError = appendProbeError(cap.ProbeError, fmt.Sprintf("model probe: %s", classifyProbeError(err)))
		}
		out.Runtimes = append(out.Runtimes, normalizeRuntimeCapability(cap))
	}
	return normalizeRuntimeCapabilities(out)
}

// ---------------------------------------------------------------------------
// Service methods (unchanged)
// ---------------------------------------------------------------------------

func (s *Service) getRuntimeCapabilities() runtimeCapabilitiesPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRuntimeCapabilities(s.capabilities)
}

func (s *Service) refreshRuntimeCapabilities(ctx context.Context) {
	innerCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	caps := detectRuntimeCapabilities(innerCtx, s.cfg)
	s.mu.Lock()
	s.capabilities = cloneRuntimeCapabilities(caps)
	s.mu.Unlock()
}
