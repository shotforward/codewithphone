package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

type claudeRunner struct {
	claudeBin      string
	claudeModel    string
	daemonBaseURL  string
	resolveBaseURL func() string
	server         *serverClient
	approvals      approvalClient
}

// claudeStreamEvent covers the stream-json events emitted by Claude Code CLI.
type claudeStreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// init event
	SessionID string `json:"session_id,omitempty"`

	// assistant event
	Message *claudeMessage `json:"message,omitempty"`

	// result event
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`

	// stream_event — only emitted when `--include-partial-messages` is
	// passed. These are raw Anthropic stream events (message_start,
	// content_block_delta, message_stop, ...). The content_block_delta /
	// text_delta variant is the one we forward as incremental UI deltas.
	Event *claudeRawStreamEvent `json:"event,omitempty"`
}

type claudeMessage struct {
	ID      string        `json:"id"`
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	ID    string          `json:"id,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// claudeRawStreamEvent mirrors the Anthropic Messages API streaming event
// schema that Claude Code re-emits under a top-level
// `{"type":"stream_event","event":{...}}` envelope when
// --include-partial-messages is enabled.
type claudeRawStreamEvent struct {
	Type  string           `json:"type"`            // message_start / content_block_delta / message_stop / ...
	Index int              `json:"index,omitempty"` // content block index
	Delta *claudeRawDelta  `json:"delta,omitempty"`
}

type claudeRawDelta struct {
	Type        string `json:"type"`                  // text_delta / input_json_delta / ...
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

func newClaudeRunner(cfg config.Config, server *serverClient, resolveBaseURL func() string) *claudeRunner {
	return &claudeRunner{
		claudeBin:      cfg.ClaudeBin,
		claudeModel:    cfg.ClaudeModel,
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

func (r *claudeRunner) RunTurn(ctx context.Context, dispatch taskDispatch, providerSessionRef string, profile turnExecutionProfile) (string, error) {
	// deltaBuf MUST be a per-RunTurn local: the runner is a singleton shared
	// across all concurrent task workers, so storing the buffer on the runner
	// struct would let two sessions overwrite each other's session/task ids
	// and cross-publish their assistant.delta events.
	deltaBuf := NewEventBuffer(r.server, dispatch.SessionID, dispatch.TaskRunID)
	defer deltaBuf.Close()

	runTurnStart := time.Now()

	// Build MCP config JSON for Claude Code to connect to our SSE endpoint
	baseURL := r.daemonBaseURL
	if r.resolveBaseURL != nil {
		if resolved := r.resolveBaseURL(); resolved != "" {
			baseURL = resolved
		}
	}
	mcpSSEURL := fmt.Sprintf("%s/mcp/sse?session=%s&task=%s",
		baseURL, dispatch.SessionID, dispatch.TaskRunID)

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"pocketcode": map[string]any{
				"type": "sse",
				"url":  mcpSSEURL,
			},
		},
	}

	// Write MCP config to a temp file
	tmpDir := filepath.Join(os.TempDir(), "pocketcode-claude")
	_ = os.MkdirAll(tmpDir, 0o755)
	mcpConfigPath := filepath.Join(tmpDir, fmt.Sprintf("mcp_%s.json", dispatch.TaskRunID))
	mcpJSON, _ := json.Marshal(mcpConfig)
	if err := os.WriteFile(mcpConfigPath, mcpJSON, 0o644); err != nil {
		return "", fmt.Errorf("write mcp config: %w", err)
	}
	defer os.Remove(mcpConfigPath)

	// Install per-task PostToolUse + PreToolUse hooks into the workspace's
	// .claude/settings.local.json so Claude reports native Write/Edit calls
	// back to the daemon (for inline diff cards) and refuses to touch paths
	// outside the workspace. Original settings.local.json is backed up and
	// restored on turn exit.
	claudeHookCleanup, claudeHookErr := installClaudeWorkspaceHooks(dispatch.WorkspaceRoot)
	if claudeHookErr != nil {
		log.Printf("[CLAUDE-HOOK] install failed taskRun=%s: %v", dispatch.TaskRunID, claudeHookErr)
	}
	defer claudeHookCleanup()

	systemPrompt := `IMPORTANT TOOL USAGE RULES:
- To execute shell commands, you MUST use the MCP tool "mcp_pocketcode_run_command". Do NOT use Bash.
- To write or modify files, use the native Edit, Write, MultiEdit, or NotebookEdit tools. Prefer Edit for targeted in-place changes; use Write only when creating a new file or fully overwriting it.
- Use Read, Grep, and Glob freely for inspection.
- Always call tools yourself in the main agent context — do not delegate to sub-agents.
- For long-running service commands (dev server, start/serve, docker compose up, watch, tail -f), set executionMode="auto" and waitTimeoutSec=120 when calling run_command.
`
	prompt := dispatch.Prompt
	if profile.ReadOnly {
		prompt += "\n\n(This is a read-only turn. Do not execute destructive commands or modify files.)"
	}

	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		// Without --include-partial-messages, Claude Code emits one
		// `{"type":"assistant","message":...}` snapshot per turn at the
		// end of generation, which makes long answers land all at once
		// in the UI. The flag opts into Anthropic-style stream events
		// (`{"type":"stream_event","event":{...}}` with
		// content_block_delta / text_delta) so we can forward
		// token-level deltas as they arrive.
		"--include-partial-messages",
		"--verbose",
		"--mcp-config", mcpConfigPath,
		"--allowedTools",
		"mcp__pocketcode__run_command",
		"Edit", "Write", "MultiEdit", "NotebookEdit",
		"Read", "Glob", "Grep",
		"WebSearch", "WebFetch",
		"--permission-mode", "default",
		"--system-prompt", systemPrompt,
	}
	model := strings.TrimSpace(dispatch.Model)
	if model == "" {
		model = strings.TrimSpace(r.claudeModel)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if providerSessionRef != "" {
		args = append(args, "--resume", providerSessionRef)
	}

	cmd := exec.CommandContext(ctx, r.claudeBin, args...)
	cmd.Dir = dispatch.WorkspaceRoot
	cmd.Env = append(os.Environ(),
		"POCKETCODE_MCP_DAEMON_URL="+baseURL,
		"POCKETCODE_MCP_SESSION_ID="+dispatch.SessionID,
		"POCKETCODE_MCP_TASK_RUN_ID="+dispatch.TaskRunID,
		"POCKETCODE_TASK_WORKSPACE="+dispatch.WorkspaceRoot,
	)
	// Send SIGTERM instead of SIGKILL on context cancel so the CLI
	// process can save its session state before exiting.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second // SIGKILL after 10s if still alive

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	log.Printf("[TIMING] claude pre-start setup: %v", time.Since(runTurnStart))
	tStart := time.Now()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	log.Printf("[TIMING] claude cmd.Start(): %v", time.Since(tStart))

	stderrTail := newStderrTailBuffer(runnerStderrTailLimit)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "ExperimentalWarning") || strings.Contains(line, "fetch") {
				continue
			}
			stderrTail.Add(line)
			log.Printf("claude stderr: %s", line)
		}
		if err := scanner.Err(); err != nil {
			stderrTail.Add("stderr scanner error: " + err.Error())
			log.Printf("claude stderr scanner error: %v", err)
		}
	}()

	var sessionID string
	firstOutput := true
	streamOpen := false
	currentAssistantItemID := ""
	fallbackAssistantIndex := 0
	claudeUnknownTypeLogged := map[string]bool{}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		if firstOutput {
			log.Printf("[TIMING] claude first stdout output: %v after start", time.Since(tStart))
			firstOutput = false
		}

		line := scanner.Bytes()
		var ev claudeStreamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch {
		case ev.Type == "system" && ev.Subtype == "init":
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}

		case ev.Type == "stream_event" && ev.Event != nil:
			// Token-level streaming path (requires --include-partial-messages).
			// We only forward text deltas; tool_use input deltas and other
			// raw events are tracked by the snapshot `assistant` event below.
			if ev.Event.Type == "content_block_delta" && ev.Event.Delta != nil &&
				ev.Event.Delta.Type == "text_delta" && ev.Event.Delta.Text != "" {
				if !streamOpen {
					_ = emitAssistantStreamStarted(ctx, r.server, dispatch, dispatch.TaskRunID)
					_ = emitTurnPhase(ctx, r.server, dispatch, turnPhaseFinalizing, nil)
					streamOpen = true
				}
				itemID := strings.TrimSpace(currentAssistantItemID)
				if itemID == "" {
					fallbackAssistantIndex++
					currentAssistantItemID = fmt.Sprintf("%s:assistant:%d", dispatch.TaskRunID, fallbackAssistantIndex)
					itemID = currentAssistantItemID
				}
				deltaBuf.Append(ctx, ev.Event.Delta.Text, itemID)
			}

		case ev.Type == "assistant" && ev.Message != nil:
			// End-of-turn snapshot. With --include-partial-messages enabled
			// the text has already been streamed via stream_event above, so
			// we only use this event to capture the canonical message.id —
			// re-appending the snapshot text would duplicate the assistant
			// reply in the timeline buffer.
			itemID := strings.TrimSpace(ev.Message.ID)
			if itemID != "" {
				currentAssistantItemID = itemID
			}
			if !streamOpen {
				// Defensive fallback: if no stream_event arrived (e.g. a
				// Claude build without partial-message support, or the
				// model produced no text), open the stream now and emit
				// the snapshot text so the UI still sees the reply.
				_ = emitAssistantStreamStarted(ctx, r.server, dispatch, dispatch.TaskRunID)
				_ = emitTurnPhase(ctx, r.server, dispatch, turnPhaseFinalizing, nil)
				streamOpen = true
				flushItemID := strings.TrimSpace(currentAssistantItemID)
				if flushItemID == "" {
					fallbackAssistantIndex++
					currentAssistantItemID = fmt.Sprintf("%s:assistant:%d", dispatch.TaskRunID, fallbackAssistantIndex)
					flushItemID = currentAssistantItemID
				}
				for _, block := range ev.Message.Content {
					if block.Type == "text" && block.Text != "" {
						deltaBuf.Append(ctx, block.Text, flushItemID)
					}
				}
			}

		case ev.Type == "result":
			deltaBuf.Flush(ctx)
			if ev.Result != "" {
				itemID := strings.TrimSpace(currentAssistantItemID)
				if itemID == "" {
					fallbackAssistantIndex++
					itemID = fmt.Sprintf("%s:assistant:%d", dispatch.TaskRunID, fallbackAssistantIndex)
				}
				_ = r.server.postEvent(ctx, daemonEvent{
					SessionID: dispatch.SessionID,
					TaskRunID: dispatch.TaskRunID,
					EventType: "assistant.message.completed",
					Payload: map[string]any{
						"itemId": itemID,
						"text":   ev.Result,
					},
				})
				currentAssistantItemID = ""
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}

		default:
			// Defensive: log unrecognized stream event types so future Claude
			// CLI additions are visible in daemon logs instead of being
			// silently dropped. Logged at most once per type per turn to
			// avoid spam.
			if !claudeUnknownTypeLogged[ev.Type] {
				claudeUnknownTypeLogged[ev.Type] = true
				log.Printf("[CLAUDE] unhandled stream event type=%q subtype=%q (taskRun=%s)", ev.Type, ev.Subtype, dispatch.TaskRunID)
			}
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		log.Printf("claude scanner error: %v", scanErr)
		stderrTail.Add("stdout scanner error: " + scanErr.Error())
	}

	waitErr := cmd.Wait()
	<-stderrDone
	if waitErr != nil {
		if streamOpen {
			_ = emitAssistantStreamEnded(ctx, r.server, dispatch, dispatch.TaskRunID)
			streamOpen = false
		}
		return sessionID, wrapRunnerError("claude process failed", waitErr, stderrTail)
	}
	if scanErr != nil {
		return sessionID, wrapRunnerError("claude stream parse failed", scanErr, stderrTail)
	}

	if streamOpen {
		_ = emitAssistantStreamEnded(ctx, r.server, dispatch, dispatch.TaskRunID)
	}
	if err := profile.RunBeforeComplete(ctx); err != nil {
		log.Printf("[CHANGESET] claude runner beforeComplete hook failed: %v", err)
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
