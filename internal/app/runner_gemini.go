package app

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
	"golang.org/x/crypto/scrypt"
)

type geminiRunner struct {
	geminiBin      string
	geminiModel    string
	sessionRoot    string
	daemonBaseURL  string
	resolveBaseURL func() string
	server         *serverClient
	approvals      approvalClient
	deltaBuf       *EventBuffer
}

const (
	runnerStderrTailLimit = 12
	geminiNoTextFallback  = "Gemini finished successfully, but returned no text content."
)

type stderrTailBuffer struct {
	mu    sync.Mutex
	lines []string
	limit int
}

func newStderrTailBuffer(limit int) *stderrTailBuffer {
	if limit <= 0 {
		limit = runnerStderrTailLimit
	}
	return &stderrTailBuffer{limit: limit}
}

func (b *stderrTailBuffer) Add(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, trimmed)
	if len(b.lines) > b.limit {
		b.lines = b.lines[len(b.lines)-b.limit:]
	}
}

func (b *stderrTailBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return ""
	}
	return strings.Join(b.lines, " | ")
}

// runnerError is a typed wrapper around a runner failure that carries the
// last few lines of the runner's stderr in a structured form. The dispatch
// layer uses errors.As to recover the tail and surface it on turn.failed
// events so users can see why a runner crashed without scraping daemon logs.
type runnerError struct {
	Prefix     string
	Err        error
	StderrTail []string
}

func (e *runnerError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	if len(e.StderrTail) == 0 {
		return fmt.Sprintf("%s: %v", e.Prefix, e.Err)
	}
	return fmt.Sprintf("%s: %v (stderr: %s)", e.Prefix, e.Err, strings.Join(e.StderrTail, " | "))
}

func (e *runnerError) Unwrap() error { return e.Err }

func wrapRunnerError(prefix string, err error, stderrTail *stderrTailBuffer) error {
	if err == nil {
		return nil
	}
	return &runnerError{
		Prefix:     prefix,
		Err:        err,
		StderrTail: stderrTailLines(stderrTail),
	}
}

// stderrTailLines snapshots the buffered stderr tail as a slice. Returns nil
// (not an empty slice) when the buffer is empty so callers can distinguish
// "no tail captured" from "tail captured but empty".
func stderrTailLines(buf *stderrTailBuffer) []string {
	if buf == nil {
		return nil
	}
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if len(buf.lines) == 0 {
		return nil
	}
	out := make([]string, len(buf.lines))
	copy(out, buf.lines)
	return out
}

func newGeminiRunner(cfg config.Config, server *serverClient, resolveBaseURL func() string) *geminiRunner {
	sessionRoot := filepath.Join(filepath.Dir(cfg.SQLitePath), "gemini-sessions")
	return &geminiRunner{
		geminiBin:      cfg.GeminiBin,
		geminiModel:    cfg.GeminiModel,
		sessionRoot:    sessionRoot,
		daemonBaseURL:  daemonBaseURLFromHTTPAddr(cfg.HTTPAddr),
		resolveBaseURL: resolveBaseURL,
		server:         server,
		approvals: approvalClient{
			BaseURL:      cfg.ServerBaseURL,
			HTTPClient:   server.httpClient(),
			PollInterval: 500 * time.Millisecond,
		},
	}
}

func (r *geminiRunner) RunTurn(ctx context.Context, dispatch taskDispatch, providerSessionRef string, profile turnExecutionProfile) (string, error) {
	r.deltaBuf = NewEventBuffer(r.server, dispatch.SessionID, dispatch.TaskRunID)
	defer r.deltaBuf.Close()

	runTurnStart := time.Now()
	geminiHome, err := filepath.Abs(filepath.Join(r.sessionRoot, safeSessionDir(dispatch.SessionID)))
	if err != nil {
		return "", fmt.Errorf("resolve gemini session home: %w", err)
	}
	geminiConfigDir := filepath.Join(geminiHome, ".gemini")
	homeDir, _ := os.UserHomeDir()
	defaultGeminiConfigDir := filepath.Join(homeDir, ".gemini")

	t0 := time.Now()
	if err := syncGeminiConfig(defaultGeminiConfigDir, geminiConfigDir); err != nil {
		return "", err
	}
	log.Printf("[TIMING] syncGeminiConfig: %v", time.Since(t0))
	if err := os.MkdirAll(filepath.Join(geminiConfigDir, "policies"), 0o755); err != nil {
		return "", fmt.Errorf("failed to setup gemini home: %w", err)
	}

	policyPath := filepath.Join(geminiConfigDir, "policies", "pocketcode.toml")
	if err := os.WriteFile(policyPath, []byte(`
[[rule]]
toolName = "run_shell_command"
decision = "deny"
priority = 100
deny_message = "Please use the daemon-approved mcp_pocketcode_run_command tool instead."

[[rule]]
toolName = "write_file"
decision = "deny"
priority = 100
deny_message = "Please use the daemon-approved mcp_pocketcode_create_file tool instead."

[[rule]]
toolName = "ask_user"
decision = "deny"
priority = 100
deny_message = "No user interactive console is available. Use mcp_pocketcode_run_command to pause for approval if executing commands."
`), 0o644); err != nil {
		return "", err
	}

	settingsPath := filepath.Join(geminiConfigDir, "settings.json")
	settings := map[string]any{}
	if payload, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(payload, &settings); err != nil {
			log.Printf("gemini settings parse failed, fallback to empty settings: %v", err)
			settings = map[string]any{}
		}
	}

	mcpServers, _ := settings["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = map[string]any{}
	}
	// Use SSE transport: Gemini CLI connects to the already-running daemon HTTP server.
	// No subprocess spawn, no MCP cold start.
	baseURL := r.daemonBaseURL
	if r.resolveBaseURL != nil {
		if resolved := r.resolveBaseURL(); resolved != "" {
			baseURL = resolved
		}
	}
	mcpSSEURL := fmt.Sprintf("%s/mcp/sse?session=%s&task=%s",
		baseURL, dispatch.SessionID, dispatch.TaskRunID)
	mcpServers["pocketcode"] = map[string]any{
		"url":   mcpSSEURL,
		"trust": true,
	}
	settings["mcpServers"] = mcpServers

	sb, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, sb, 0o644); err != nil {
		return "", err
	}

	systemInstructions := `IMPORTANT TOOL USAGE RULES:
- To execute shell commands, you MUST use the MCP tool "mcp_pocketcode_run_command" directly. This is the ONLY tool available for shell execution.
- To write files, you MUST use the MCP tool "mcp_pocketcode_create_file" directly. This is the ONLY tool available for file writing.
- Do NOT attempt to use "run_shell_command", "write_file", or any built-in shell/file tools — they are disabled.
- Do NOT delegate shell command execution or file writes to sub-agents (e.g., generalist, codebase_investigator). Sub-agents cannot access MCP tools.
- Always call MCP tools yourself in the main agent context.
- For long-running service commands (dev server, start/serve, docker compose up, watch, tail -f), call run_command with executionMode="auto" and waitTimeoutSec=120.

`
	prompt := systemInstructions + dispatch.Prompt
	if profile.ReadOnly {
		prompt += "\n\n(This is a read-only turn. Do not execute destructive commands.)"
	}

	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
	}
	model := strings.TrimSpace(dispatch.Model)
	if model == "" {
		model = strings.TrimSpace(r.geminiModel)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if profile.ReadOnly {
		args = append(args, "--approval-mode", "plan")
	} else {
		args = append(args, "--approval-mode", "yolo", "--sandbox=false")
	}
	if providerSessionRef != "" {
		args = append(args, "--resume", providerSessionRef)
	}

	cmd := exec.CommandContext(ctx, r.geminiBin, args...)
	cmd.Dir = dispatch.WorkspaceRoot
	cmd.Env = append(os.Environ(), "GEMINI_CLI_HOME="+geminiHome)
	// Send SIGTERM instead of SIGKILL on context cancel so the CLI
	// process can save its session state before exiting.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second

	// Ensure GEMINI_API_KEY is set when auth type is "gemini-api-key".
	// The Gemini CLI's validateAuthMethod checks for this env var before
	// it ever reads the encrypted credentials file, so we must supply it.
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		cmd.Env = append(cmd.Env, "GEMINI_API_KEY="+key)
	} else if key, err := loadGeminiAPIKeyFromCredentials(); err == nil && key != "" {
		cmd.Env = append(cmd.Env, "GEMINI_API_KEY="+key)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	log.Printf("[TIMING] gemini pre-start setup: %v", time.Since(runTurnStart))
	tStart := time.Now()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	log.Printf("[TIMING] gemini cmd.Start(): %v", time.Since(tStart))

	stderrTail := newStderrTailBuffer(runnerStderrTailLimit)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "libsecret") || strings.Contains(line, "FileKeychain") || strings.Contains(line, "Loaded cached credentials") {
				continue
			}
			stderrTail.Add(line)
			log.Printf("gemini stderr: %s", line)
		}
		if err := scanner.Err(); err != nil {
			stderrTail.Add("stderr scanner error: " + err.Error())
			log.Printf("gemini stderr scanner error: %v", err)
		}
	}()

	var sessionID string
	firstOutput := true
	tFirstOutput := tStart
	streamOpen := false
	currentAssistantItemID := ""
	fallbackAssistantIndex := 0
	geminiUnknownTypeLogged := map[string]bool{}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		if firstOutput {
			log.Printf("[TIMING] gemini first stdout output: %v after start", time.Since(tStart))
			firstOutput = false
		}
		line := scanner.Bytes()
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		eventType := strings.TrimSpace(strings.ToLower(stringValue(raw["type"])))

		switch {
		case eventType == "init":
			if sid, ok := raw["session_id"].(string); ok && strings.TrimSpace(sid) != "" {
				sessionID = sid
			}
		case eventType == "message" && strings.EqualFold(strings.TrimSpace(stringValue(raw["role"])), "assistant"):
			text := extractGeminiAssistantText(raw)
			if strings.TrimSpace(text) == "" {
				continue
			}
			itemID := extractGeminiAssistantItemID(raw)
			if strings.TrimSpace(itemID) == "" {
				if !boolValue(raw["delta"]) || strings.TrimSpace(currentAssistantItemID) == "" {
					fallbackAssistantIndex++
					currentAssistantItemID = fmt.Sprintf("%s:assistant:%d", dispatch.TaskRunID, fallbackAssistantIndex)
				}
				itemID = currentAssistantItemID
			} else {
				currentAssistantItemID = strings.TrimSpace(itemID)
				itemID = currentAssistantItemID
			}
			if !streamOpen {
				_ = emitAssistantStreamStarted(ctx, r.server, dispatch, dispatch.TaskRunID)
				_ = emitTurnPhase(ctx, r.server, dispatch, turnPhaseFinalizing, nil)
				streamOpen = true
			}
			if boolValue(raw["delta"]) {
				r.deltaBuf.Append(ctx, text, itemID)
			} else {
				r.deltaBuf.Flush(ctx)
				if err := r.server.postEvent(ctx, daemonEvent{
					SessionID: dispatch.SessionID,
					TaskRunID: dispatch.TaskRunID,
					EventType: "assistant.message.completed",
					Payload: map[string]any{
						"itemId": itemID,
						"text":   text,
					},
				}); err != nil {
					log.Printf("gemini postEvent assistant.message.completed error: %v", err)
				}
			}
		case eventType == "result":
			if status := strings.TrimSpace(strings.ToLower(stringValue(raw["status"]))); status != "" && status != "success" {
				log.Printf("[GEMINI] non-success result status=%q (taskRun=%s) — dropping", status, dispatch.TaskRunID)
				continue
			}
			text := extractGeminiResultText(raw)
			if strings.TrimSpace(text) == "" {
				text = geminiNoTextFallback
			}
			if !streamOpen {
				_ = emitAssistantStreamStarted(ctx, r.server, dispatch, dispatch.TaskRunID)
				_ = emitTurnPhase(ctx, r.server, dispatch, turnPhaseFinalizing, nil)
				streamOpen = true
			}
			itemID := strings.TrimSpace(currentAssistantItemID)
			if itemID == "" {
				fallbackAssistantIndex++
				itemID = fmt.Sprintf("%s:assistant:%d", dispatch.TaskRunID, fallbackAssistantIndex)
			}
			if err := r.server.postEvent(ctx, daemonEvent{
				SessionID: dispatch.SessionID,
				TaskRunID: dispatch.TaskRunID,
				EventType: "assistant.message.completed",
				Payload: map[string]any{
					"itemId": itemID,
					"text":   text,
				},
			}); err != nil {
				log.Printf("gemini postEvent assistant.message.completed fallback error: %v", err)
			}
		default:
			// Defensive: log unrecognized event types so future Gemini CLI
			// additions are visible in daemon logs instead of being silently
			// dropped. Logged at most once per type per turn.
			if !geminiUnknownTypeLogged[eventType] {
				geminiUnknownTypeLogged[eventType] = true
				log.Printf("[GEMINI] unhandled stream event type=%q (taskRun=%s)", eventType, dispatch.TaskRunID)
			}
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		log.Printf("gemini scanner error: %v", scanErr)
		stderrTail.Add("stdout scanner error: " + scanErr.Error())
	}

	log.Printf("[TIMING] gemini stream processing done: %v after first output", time.Since(tFirstOutput))
	waitErr := cmd.Wait()
	<-stderrDone
	if waitErr != nil {
		if streamOpen {
			_ = emitAssistantStreamEnded(ctx, r.server, dispatch, dispatch.TaskRunID)
			streamOpen = false
		}
		return sessionID, wrapRunnerError("gemini process failed", waitErr, stderrTail)
	}
	if scanErr != nil {
		return sessionID, wrapRunnerError("gemini stream parse failed", scanErr, stderrTail)
	}

	if streamOpen {
		_ = emitAssistantStreamEnded(ctx, r.server, dispatch, dispatch.TaskRunID)
	}
	if err := profile.RunBeforeComplete(ctx); err != nil {
		log.Printf("[CHANGESET] gemini runner beforeComplete hook failed: %v", err)
	}
	_ = r.server.postEvent(ctx, daemonEvent{
		SessionID: dispatch.SessionID,
		TaskRunID: dispatch.TaskRunID,
		EventType: "turn.completed",
		Payload: map[string]any{
			"status": "completed",
		},
	})

	return sessionID, nil
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func extractGeminiAssistantText(raw map[string]any) string {
	if text := collectGeminiText(raw["content"]); strings.TrimSpace(text) != "" {
		return text
	}
	if text := collectGeminiText(raw["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	message, _ := raw["message"].(map[string]any)
	if text := collectGeminiText(message["content"]); strings.TrimSpace(text) != "" {
		return text
	}
	if text := collectGeminiText(message["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	return ""
}

func extractGeminiAssistantItemID(raw map[string]any) string {
	for _, key := range []string{"itemId", "item_id", "id", "messageId", "message_id"} {
		if value := strings.TrimSpace(stringValue(raw[key])); value != "" {
			return value
		}
	}
	message, _ := raw["message"].(map[string]any)
	for _, key := range []string{"itemId", "item_id", "id", "messageId", "message_id"} {
		if value := strings.TrimSpace(stringValue(message[key])); value != "" {
			return value
		}
	}
	return ""
}

func extractGeminiResultText(raw map[string]any) string {
	if text := collectGeminiText(raw["result"]); strings.TrimSpace(text) != "" {
		return text
	}
	if text := collectGeminiText(raw["output"]); strings.TrimSpace(text) != "" {
		return text
	}
	if text := collectGeminiText(raw["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	message, _ := raw["message"].(map[string]any)
	if text := collectGeminiText(message["content"]); strings.TrimSpace(text) != "" {
		return text
	}
	if text := collectGeminiText(message["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	return ""
}

func collectGeminiText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := collectGeminiText(item); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	case map[string]any:
		for _, key := range []string{"text", "content", "parts", "output", "result"} {
			if text := collectGeminiText(typed[key]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func daemonBaseURLFromHTTPAddr(httpAddr string) string {
	addr := strings.TrimSpace(httpAddr)
	if addr == "" {
		return "http://127.0.0.1:8081"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// skipDirs contains directories that should not be synced (large/ephemeral data).
var syncSkipDirs = map[string]bool{
	"history":  true,
	"tmp":      true,
	"policies": true, // we write our own policies
}

func syncGeminiConfig(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("prepare gemini config dir: %w", err)
	}

	info, err := os.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat default gemini config: %w", err)
	}
	if !info.IsDir() {
		return nil
	}

	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Skip large/ephemeral directories
		if d.IsDir() && syncSkipDirs[d.Name()] {
			return filepath.SkipDir
		}

		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		srcInfo, err := d.Info()
		if err != nil {
			return err
		}
		if !srcInfo.Mode().IsRegular() {
			return nil
		}

		// Skip if target is already up-to-date (same size and mod time)
		if dstInfo, dstErr := os.Stat(target); dstErr == nil {
			if dstInfo.Size() == srcInfo.Size() && !srcInfo.ModTime().After(dstInfo.ModTime()) {
				return nil
			}
		}

		return copyRegularFile(path, target, srcInfo.Mode().Perm())
	})
}

func copyRegularFile(srcPath, dstPath string, perm fs.FileMode) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func safeSessionDir(sessionID string) string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return "unknown-session"
	}
	cleaned := strings.ReplaceAll(trimmed, "/", "_")
	cleaned = strings.ReplaceAll(cleaned, "\\", "_")
	return cleaned
}

// ---------------------------------------------------------------------------
// Gemini credential helpers
// ---------------------------------------------------------------------------

// geminiAuthType reads the auth type from ~/.gemini/settings.json.
func geminiAuthType() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if err != nil {
		return ""
	}
	var settings struct {
		Security struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	return settings.Security.Auth.SelectedType
}

// loadGeminiAPIKeyFromCredentials decrypts the API key stored in
// ~/.gemini/gemini-credentials.json by the Gemini CLI's FileKeychain.
//
// The file is AES-256-GCM encrypted with a key derived via:
//
//	scrypt("gemini-cli-oauth", "${hostname}-${username}-gemini-cli", 32)
//
// The ciphertext format is "iv_hex:authTag_hex:ciphertext_hex".
// The plaintext is a JSON map; the API key lives at
// .<entry>.token.accessToken where <entry> is "gemini-api-key".
func loadGeminiAPIKeyFromCredentials() (string, error) {
	if geminiAuthType() != "gemini-api-key" {
		return "", nil // not using API key auth
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	credPath := filepath.Join(home, ".gemini", "gemini-credentials.json")
	raw, err := os.ReadFile(credPath)
	if err != nil {
		return "", err
	}

	plaintext, err := decryptGeminiCredentials(string(raw))
	if err != nil {
		return "", fmt.Errorf("decrypt gemini credentials: %w", err)
	}

	// The decrypted JSON is a nested structure where the outer key is the
	// keychain account ("gemini-cli-api-key"), and the inner key is the
	// entry name ("default-api-key"). The inner value is a JSON *string*
	// containing the actual credential object.
	//
	// Example:
	// {
	//   "gemini-cli-api-key": {
	//     "default-api-key": "{\"serverName\":\"default-api-key\",\"token\":{\"accessToken\":\"AIza...\"}}"
	//   }
	// }
	var store map[string]map[string]string
	if err := json.Unmarshal([]byte(plaintext), &store); err != nil {
		return "", fmt.Errorf("parse gemini credentials: %w", err)
	}
	// Look through all accounts for an entry containing a token.
	for _, entries := range store {
		for _, raw := range entries {
			var cred struct {
				Token struct {
					AccessToken string `json:"accessToken"`
					TokenType   string `json:"tokenType"`
				} `json:"token"`
			}
			if err := json.Unmarshal([]byte(raw), &cred); err != nil {
				continue
			}
			if cred.Token.AccessToken != "" {
				return cred.Token.AccessToken, nil
			}
		}
	}
	return "", fmt.Errorf("no API key found in gemini credentials")
}

// decryptGeminiCredentials decrypts data produced by the Gemini CLI
// FileKeychain (AES-256-GCM, format: "iv:authTag:ciphertext" in hex).
func decryptGeminiCredentials(encrypted string) (string, error) {
	parts := strings.SplitN(encrypted, ":", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid encrypted format: expected 3 colon-separated parts")
	}
	iv, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decode iv: %w", err)
	}
	authTag, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode authTag: %w", err)
	}
	ciphertext, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}

	key, err := geminiEncryptionKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
	if err != nil {
		return "", err
	}

	// GCM expects ciphertext || authTag as input.
	sealed := append(ciphertext, authTag...)
	plaintext, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("gcm decrypt: %w", err)
	}
	return string(plaintext), nil
}

// geminiEncryptionKey derives the same key the Gemini CLI uses:
//
//	scrypt("gemini-cli-oauth", "${hostname}-${username}-gemini-cli", 32)
func geminiEncryptionKey() ([]byte, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("get hostname: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}
	salt := fmt.Sprintf("%s-%s-gemini-cli", hostname, u.Username)
	// scrypt params: N=16384, r=8, p=1, keyLen=32 (Node.js crypto.scryptSync defaults)
	key, err := scrypt.Key([]byte("gemini-cli-oauth"), []byte(salt), 16384, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("scrypt key derivation: %w", err)
	}
	return key, nil
}
