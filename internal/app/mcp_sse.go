package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// mcpSSESession is an active SSE connection with a channel for outbound messages.
type mcpSSESession struct {
	outCh     chan []byte
	sessionID string // PocketCode session
	taskRunID string // PocketCode task
}

// mcpSSERegistry tracks active SSE sessions.
type mcpSSERegistry struct {
	mu       sync.RWMutex
	sessions map[string]*mcpSSESession // keyed by connection ID
	nextID   int64
}

var sseRegistry = &mcpSSERegistry{sessions: make(map[string]*mcpSSESession)}

const mcpSSEAuditPrefix = "[MCP_SSE_AUDIT]"

func sseRegistryConnID() string {
	sseRegistry.mu.Lock()
	sseRegistry.nextID++
	id := fmt.Sprintf("sse_%d", sseRegistry.nextID)
	sseRegistry.mu.Unlock()
	return id
}

// handleMCPSSE serves GET /mcp/sse?session={sid}&task={tid}
func (s *Service) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sessionID := r.URL.Query().Get("session")
	taskRunID := r.URL.Query().Get("task")
	connID := sseRegistryConnID()

	sess := &mcpSSESession{
		outCh:     make(chan []byte, 64),
		sessionID: sessionID,
		taskRunID: taskRunID,
	}

	sseRegistry.mu.Lock()
	sseRegistry.sessions[connID] = sess
	sseRegistry.mu.Unlock()

	defer func() {
		sseRegistry.mu.Lock()
		delete(sseRegistry.sessions, connID)
		sseRegistry.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Tell client where to POST JSON-RPC messages.
	postURL := fmt.Sprintf("/mcp/message?c=%s", connID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", postURL)
	flusher.Flush()

	log.Printf("[MCP-SSE] connected connID=%s session=%s task=%s", connID, sessionID, taskRunID)

	ctx := r.Context()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[MCP-SSE] disconnected connID=%s", connID)
			return
		case msg := <-sess.outCh:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleMCPMessage serves POST /mcp/message?c={connID}
func (s *Service) handleMCPMessage(w http.ResponseWriter, r *http.Request) {
	connID := r.URL.Query().Get("c")

	sseRegistry.mu.RLock()
	sess, ok := sseRegistry.sessions[connID]
	sseRegistry.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown connection", http.StatusNotFound)
		return
	}

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[MCP-SSE] method=%s connID=%s", req.Method, connID)

	switch req.Method {
	case "initialize":
		sendSSEResponse(sess, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "pocketcode", "version": "1.0.0"},
		})

	case "notifications/initialized":
		// no response

	case "tools/list":
		sendSSEResponse(sess, req.ID, map[string]any{
			"tools": mcpToolsList(),
		})

	case "tools/call":
		var params mcpToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			logMCPSSEAudit(sess.sessionID, sess.taskRunID, params.Name, "", "", "tools_call_invalid_params", "error", err)
			sendSSEError(sess, req.ID, -32602, "invalid params")
			break
		}
		dispatch := taskDispatch{SessionID: sess.sessionID, TaskRunID: sess.taskRunID}
		toolCallID := fmt.Sprintf("tool_%d", time.Now().UnixNano())
		callStartedAt := time.Now()
		toolSummary := truncateForLog(string(params.Arguments), 180)
		log.Printf("[MCP-SSE] tools/call connID=%s tool=%s session=%s task=%s args=%s",
			connID, params.Name, sess.sessionID, sess.taskRunID, truncateForLog(string(params.Arguments), 280))
		logMCPSSEAudit(sess.sessionID, sess.taskRunID, params.Name, "", "", "tools_call_received", "accepted", nil)
		_ = emitToolCallStarted(context.Background(), &s.serverClient, dispatch, toolCallID, params.Name, map[string]any{
			"summary": toolSummary,
		})
		_ = emitTurnPhase(context.Background(), &s.serverClient, dispatch, turnPhaseRunningTools, nil)
		// Handle tool call asynchronously — Gemini expects HTTP 202 quickly,
		// then result comes via SSE.
		go func(reqID json.RawMessage, toolName string, params mcpToolCallParams, sessionID, taskRunID, callID string, startedAt time.Time) {
			// Do not tie execution lifetime to this HTTP request context:
			// MCP clients expect the result to be pushed asynchronously over SSE.
			execCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			result := s.executeMCPToolCall(execCtx, sessionID, taskRunID, params)
			status := "ok"
			if resultIsError(result) {
				status = "error"
			}
			dispatch := taskDispatch{SessionID: sessionID, TaskRunID: taskRunID}
			finishedStatus := "success"
			if status == "error" {
				finishedStatus = "failed"
			}
			_ = emitToolCallFinished(context.Background(), &s.serverClient, dispatch, callID, toolName, finishedStatus, time.Since(startedAt), nil)
			_ = emitTurnPhase(context.Background(), &s.serverClient, dispatch, turnPhaseAnalyzing, nil)
			logMCPSSEAudit(sessionID, taskRunID, toolName, "", "", "tools_call_finished", status, nil)
			log.Printf("[MCP-SSE] tools/call finished tool=%s session=%s task=%s isError=%t",
				toolName, sessionID, taskRunID, resultIsError(result))
			sendSSEResponse(sess, reqID, result)
		}(append(json.RawMessage(nil), req.ID...), params.Name, params, sess.sessionID, sess.taskRunID, toolCallID, callStartedAt)

	default:
		log.Printf("[MCP-SSE] unhandled method: %s", req.Method)
	}

	w.WriteHeader(http.StatusAccepted)
}

func sendSSEResponse(sess *mcpSSESession, id json.RawMessage, result any) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
	data, _ := json.Marshal(resp)
	select {
	case sess.outCh <- data:
	default:
		log.Printf("[MCP-SSE] outbound channel full, dropping response")
	}
}

func sendSSEError(sess *mcpSSESession, id json.RawMessage, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   map[string]any{"code": code, "message": message},
	}
	data, _ := json.Marshal(resp)
	select {
	case sess.outCh <- data:
	default:
	}
}

// mcpToolsList returns the MCP tools definition.
func mcpToolsList() []map[string]any {
	return []map[string]any{
		{
			"name":        "run_command",
			"description": "Execute a shell command. Use this tool ONLY. You MUST use this tool to run shell commands.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"command"},
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "The shell command to run"},
					"cwd":     map[string]any{"type": "string"},
					"reason":  map[string]any{"type": "string"},
					"executionMode": map[string]any{
						"type":        "string",
						"description": "Execution mode: wait, auto, or background. Default is wait.",
						"enum":        []string{runCommandExecutionModeWait, runCommandExecutionModeAuto, runCommandExecutionModeBackground},
					},
					"waitTimeoutSec": map[string]any{
						"type":        "integer",
						"description": "Only for auto/wait mode. Seconds to wait in foreground before timeout or auto-detach.",
					},
					"background": map[string]any{
						"type":        "boolean",
						"description": "Alias for executionMode=background.",
					},
				},
			},
		},
		{
			"name":        "create_file",
			"description": "Write a file. You MUST use this tool to write files.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"path", "content"},
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
			},
		},
	}
}

// executeMCPToolCall routes a tool call to the appropriate handler.
func (s *Service) executeMCPToolCall(ctx context.Context, sessionID, taskRunID string, params mcpToolCallParams) map[string]any {
	tcReq := toolCallRequest{
		SessionID: sessionID,
		TaskRunID: taskRunID,
		ToolName:  params.Name,
		Arguments: params.Arguments,
	}

	if tcReq.ToolName == "create_file" || tcReq.ToolName == "write_file" || tcReq.ToolName == "pocketcode_write_file" {
		return s.executeWriteFileTool(ctx, tcReq)
	}
	if tcReq.ToolName == "run_command" || tcReq.ToolName == "pocketcode_run_command" {
		return s.executeRunCommandTool(ctx, tcReq)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": "unsupported tool: " + tcReq.ToolName}},
		"isError": true,
	}
}

func (s *Service) executeRunCommandTool(ctx context.Context, req toolCallRequest) map[string]any {
	toolName := req.ToolName
	if strings.TrimSpace(toolName) == "" {
		toolName = "run_command"
	}

	parsedArgs, err := parseRunCommandToolArgs(req.Arguments)
	if err != nil {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", "", "run_command_parse", "error", err)
		log.Printf("[MCP-SSE] run_command invalid args session=%s task=%s err=%v raw=%s",
			req.SessionID, req.TaskRunID, err, truncateForLog(string(req.Arguments), 320))
		return map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": "invalid arguments: " + err.Error(),
			}},
			"isError": true,
		}
	}

	rawCommand := parsedArgs.RawCommand
	cwd := parsedArgs.CWD
	logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", "", "run_command_parse", "ok", nil)
	log.Printf("[MCP-SSE] run_command parsed session=%s task=%s source=%s mode=%s timeout=%d keys=%v command=%s cwd=%s",
		req.SessionID, req.TaskRunID, parsedArgs.CommandSource, parsedArgs.ExecutionMode, parsedArgs.WaitTimeoutSec, parsedArgs.Keys, truncateForLog(rawCommand, 180), cwd)

	preflight := runCommandPreflight(rawCommand, parsedArgs.ExecutionMode, parsedArgs.WaitTimeoutSec)
	if !preflight.Allow {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", "", "run_command_preflight", "blocked", nil)
		return map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": preflight.RejectReason,
			}},
			"isError": true,
		}
	}
	rawCommand = preflight.Command
	effectiveMode := preflight.ExecutionMode
	effectiveWaitTimeoutSec := preflight.WaitTimeoutSec
	command := normalizeCommandText(rawCommand, cwd, parsedArgs.Reason)
	commandRunID := fmt.Sprintf("cmd_%d", time.Now().UnixNano())
	execCWD := s.resolveRunCommandExecCWD(req, command.CWD)
	logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", commandRunID, "resolve_cwd", execCWD, nil)

	if profile, ok := s.getTaskProfile(req.TaskRunID); ok && !allowsCommandForProfile(profile, command) {
		message := fmt.Sprintf("blocked in read-only turn: command risk level is %s", command.RiskLevel)
		logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", commandRunID, "command_profile_gate", "blocked", nil)
		_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, commandExecutionResult{
			Status:   "declined",
			DenyType: commandDenyTypePolicy,
			ExitCode: 1,
			Output:   message,
		}, "")
		return map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": message,
			}},
			"isError": true,
		}
	}
	if approved, status, decisionErr := s.awaitCommandApproval(ctx, req, command, commandRunID, rawCommand); decisionErr != nil || !approved {
		output := "command approval denied"
		denyType := commandDenyTypeFromApproval(status, decisionErr)
		if status != nil {
			output = deniedMessageFromApproval(*status)
		}
		if decisionErr != nil {
			output = "command approval failed: " + decisionErr.Error()
		}
		_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, commandExecutionResult{
			Status:   "declined",
			DenyType: denyType,
			ExitCode: 1,
			Output:   output,
			WaitErr:  decisionErr,
		}, "")
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": output}},
			"isError": true,
		}
	}

	if err := s.serverClient.postEvent(ctx, daemonEvent{
		SessionID: req.SessionID,
		TaskRunID: req.TaskRunID,
		EventType: "command.started",
		Payload: map[string]any{
			"commandRunId":   commandRunID,
			"command":        rawCommand,
			"rawOriginal":    parsedArgs.RawCommand,
			"cwd":            execCWD,
			"mode":           effectiveMode,
			"waitTimeoutSec": effectiveWaitTimeoutSec,
		},
	}); err != nil {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", commandRunID, "command_started_emit", "error", err)
		log.Printf("[MCP-SSE] postEvent command.started failed session=%s task=%s commandRun=%s err=%v",
			req.SessionID, req.TaskRunID, commandRunID, err)
	} else {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", commandRunID, "command_started_emit", "ok", nil)
	}

	execution, err := startCommandExecution(rawCommand, execCWD, commandRunID)
	if err != nil {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", commandRunID, "command_start", "error", err)
		_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, commandExecutionResult{
			Status:   "failed",
			ExitCode: 1,
			Output:   "failed to start command: " + err.Error(),
			WaitErr:  err,
		}, "")
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": "failed to start command: " + err.Error()}},
			"isError": true,
		}
	}
	logMCPSSEAudit(req.SessionID, req.TaskRunID, toolName, "", commandRunID, "command_start", "ok", nil)

	backgroundRun := backgroundCommandRun{
		CommandRunID: commandRunID,
		SessionID:    req.SessionID,
		TaskRunID:    req.TaskRunID,
		Command:      rawCommand,
		CWD:          execCWD,
		LogPath:      execution.logPath,
		PID:          execution.cmd.Process.Pid,
		StartedAt:    execution.started,
	}
	s.registerRunningCommand(backgroundRun, execution)

	switch effectiveMode {
	case runCommandExecutionModeBackground:
		if err := s.reserveBackgroundCommand(backgroundRun); err != nil {
			execution.terminate()
			result := <-execution.resultCh
			s.releaseRunningCommand(commandRunID)
			result.Status = "failed"
			result.Output = appendCommandResultMessage(result.Output, err.Error())
			_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}
		}
		s.startDetachedCommandWatcher(req, backgroundRun, execution)
		_ = s.postCommandDetachedEvent(ctx, req, backgroundRun, effectiveMode, effectiveWaitTimeoutSec)
		return backgroundCommandToolResult(backgroundRun, effectiveMode, effectiveWaitTimeoutSec, 0)

	case runCommandExecutionModeAuto:
		waitTimeoutSec := effectiveWaitTimeoutSec
		if waitTimeoutSec <= 0 {
			waitTimeoutSec = defaultAutoWaitTimeoutSec
		}
		waitTimer := time.NewTimer(time.Duration(waitTimeoutSec) * time.Second)
		defer waitTimer.Stop()

		select {
		case result := <-execution.resultCh:
			s.releaseRunningCommand(commandRunID)
			_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
			return map[string]any{"content": []map[string]any{{"type": "text", "text": result.Output}}}
		case <-waitTimer.C:
			if !shouldAutoDetachCommand(rawCommand) {
				execution.terminate()
				result := <-execution.resultCh
				s.releaseRunningCommand(commandRunID)
				result.Status = "failed"
				result.Output = appendCommandResultMessage(result.Output, fmt.Sprintf("command timed out after %ds", waitTimeoutSec))
				_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
				return map[string]any{
					"content": []map[string]any{{"type": "text", "text": result.Output}},
					"isError": true,
				}
			}
			if err := s.reserveBackgroundCommand(backgroundRun); err != nil {
				execution.terminate()
				result := <-execution.resultCh
				s.releaseRunningCommand(commandRunID)
				result.Status = "failed"
				result.Output = appendCommandResultMessage(result.Output, err.Error())
				_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
				return map[string]any{
					"content": []map[string]any{{"type": "text", "text": err.Error()}},
					"isError": true,
				}
			}
			s.startDetachedCommandWatcher(req, backgroundRun, execution)
			_ = s.postCommandDetachedEvent(ctx, req, backgroundRun, effectiveMode, waitTimeoutSec)
			return backgroundCommandToolResult(backgroundRun, effectiveMode, waitTimeoutSec, waitTimeoutSec)
		case <-ctx.Done():
			execution.terminate()
			result := <-execution.resultCh
			s.releaseRunningCommand(commandRunID)
			result.Status = "failed"
			result.Output = appendCommandResultMessage(result.Output, "command cancelled by context")
			_ = s.postCommandFinishedEvent(context.Background(), req, commandRunID, rawCommand, execCWD, result, execution.logPath)
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": result.Output}},
				"isError": true,
			}
		}
	default:
		if effectiveWaitTimeoutSec > 0 {
			waitTimer := time.NewTimer(time.Duration(effectiveWaitTimeoutSec) * time.Second)
			defer waitTimer.Stop()
			select {
			case result := <-execution.resultCh:
				s.releaseRunningCommand(commandRunID)
				_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
				return map[string]any{"content": []map[string]any{{"type": "text", "text": result.Output}}}
			case <-waitTimer.C:
				execution.terminate()
				result := <-execution.resultCh
				s.releaseRunningCommand(commandRunID)
				result.Status = "failed"
				result.Output = appendCommandResultMessage(result.Output, fmt.Sprintf("command timed out after %ds", effectiveWaitTimeoutSec))
				_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
				return map[string]any{
					"content": []map[string]any{{"type": "text", "text": result.Output}},
					"isError": true,
				}
			case <-ctx.Done():
				execution.terminate()
				result := <-execution.resultCh
				s.releaseRunningCommand(commandRunID)
				result.Status = "failed"
				result.Output = appendCommandResultMessage(result.Output, "command cancelled by context")
				_ = s.postCommandFinishedEvent(context.Background(), req, commandRunID, rawCommand, execCWD, result, execution.logPath)
				return map[string]any{
					"content": []map[string]any{{"type": "text", "text": result.Output}},
					"isError": true,
				}
			}
		}
		result := <-execution.resultCh
		s.releaseRunningCommand(commandRunID)
		_ = s.postCommandFinishedEvent(ctx, req, commandRunID, rawCommand, execCWD, result, execution.logPath)
		return map[string]any{"content": []map[string]any{{"type": "text", "text": result.Output}}}
	}
}

type runCommandPreflightDecision struct {
	Allow          bool
	RejectReason   string
	Command        string
	ExecutionMode  string
	WaitTimeoutSec int
}

func runCommandPreflight(rawCommand, requestedMode string, requestedWaitTimeoutSec int) runCommandPreflightDecision {
	trimmed := strings.TrimSpace(rawCommand)
	if trimmed == "" {
		return runCommandPreflightDecision{
			Allow:        false,
			RejectReason: "command is required",
		}
	}

	normalized := trimmed
	if unwrapped, wrapped, rejectReason := unwrapShellWrapperCommand(trimmed); rejectReason != "" {
		return runCommandPreflightDecision{
			Allow:        false,
			RejectReason: rejectReason,
		}
	} else if wrapped {
		normalized = unwrapped
	}

	effectiveMode := strings.TrimSpace(requestedMode)
	if effectiveMode == "" {
		effectiveMode = runCommandExecutionModeWait
	}
	effectiveWaitTimeoutSec := requestedWaitTimeoutSec

	normalizedCommand := normalizeCommandText(normalized, ".", "")
	if normalizedCommand.RiskLevel == riskLevelSafeRead {
		effectiveMode = runCommandExecutionModeWait
		if effectiveWaitTimeoutSec <= 0 {
			effectiveWaitTimeoutSec = 25
		}
		if effectiveWaitTimeoutSec > 60 {
			effectiveWaitTimeoutSec = 60
		}
	} else if effectiveMode == runCommandExecutionModeAuto && effectiveWaitTimeoutSec <= 0 {
		effectiveWaitTimeoutSec = defaultAutoWaitTimeoutSec
	} else if effectiveMode == runCommandExecutionModeWait && effectiveWaitTimeoutSec <= 0 {
		effectiveWaitTimeoutSec = 60
	}

	return runCommandPreflightDecision{
		Allow:          true,
		Command:        normalized,
		ExecutionMode:  effectiveMode,
		WaitTimeoutSec: effectiveWaitTimeoutSec,
	}
}

func unwrapShellWrapperCommand(rawCommand string) (command string, wrapped bool, rejectReason string) {
	fields := splitShellWords(rawCommand)
	if len(fields) == 0 {
		return "", false, ""
	}

	if !isShellWrapper(fields[0]) {
		return strings.TrimSpace(rawCommand), false, ""
	}

	args := fields[1:]
	if len(args) == 0 {
		return "", true, "interactive shell wrapper is not supported. Please run the target command directly."
	}

	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "-i" || arg == "--interactive" || arg == "-s" {
			return "", true, "interactive shell wrapper is not supported. Please run the target command directly."
		}
		if strings.HasPrefix(arg, "-") && strings.Contains(arg, "i") && arg != "-c" && arg != "-lc" && arg != "-cl" && arg != "-l" {
			return "", true, "interactive shell wrapper is not supported. Please run the target command directly."
		}
	}

	for idx := 0; idx < len(args); idx++ {
		switch args[idx] {
		case "-c", "-lc", "-cl":
			if idx+1 >= len(args) {
				return "", true, "shell wrapper is missing command body after -c/-lc"
			}
			body := strings.TrimSpace(args[idx+1])
			if body == "" {
				return "", true, "shell wrapper is missing command body after -c/-lc"
			}
			return body, true, ""
		}
	}

	return "", true, "shell wrapper command is not supported. Please run the target command directly."
}

func (s *Service) awaitCommandApproval(ctx context.Context, req toolCallRequest, command normalizedCommand, commandRunID, rawCommand string) (bool, *approvalStatus, error) {
	if shouldAutoApprove(command) {
		return true, nil, nil
	}
	if strings.TrimSpace(req.TaskRunID) == "" || strings.TrimSpace(req.SessionID) == "" {
		return false, nil, fmt.Errorf("approval requires task/session context")
	}
	if denied, ok := s.getTaskDeniedApproval(req.TaskRunID, command.Fingerprint); ok {
		return false, &denied, nil
	}

	actionID := approvalActionIDFromRequest(req.TaskRunID, "", json.RawMessage(`"`+commandRunID+`"`))
	dispatch := taskDispatch{SessionID: req.SessionID, TaskRunID: req.TaskRunID}
	_ = emitTurnBlocked(ctx, &s.serverClient, dispatch, "awaiting_approval", map[string]any{
		"mode":             "manual",
		"approvalActionId": actionID,
		"riskLevel":        command.RiskLevel,
	})
	defer func() {
		_ = emitTurnUnblocked(context.Background(), &s.serverClient, dispatch, "awaiting_approval", map[string]any{
			"approvalActionId": actionID,
		})
	}()

	if err := s.serverClient.postEvent(ctx, daemonEvent{
		SessionID: req.SessionID,
		TaskRunID: req.TaskRunID,
		EventType: "command.permission_requested",
		Payload: map[string]any{
			"approvalActionId":   actionID,
			"commandRunId":       commandRunID,
			"executable":         command.Executable,
			"args":               command.Args,
			"cwd":                command.CWD,
			"rawCommand":         rawCommand,
			"reason":             command.Reason,
			"riskLevel":          command.RiskLevel,
			"commandFingerprint": command.Fingerprint,
		},
	}); err != nil {
		return false, nil, err
	}

	client := approvalClient{
		BaseURL:      s.serverClient.BaseURL,
		HTTPClient:   s.serverClient.httpClient(),
		PollInterval: 500 * time.Millisecond,
		MachineID:    s.serverClient.MachineID,
		MachineToken: s.serverClient.MachineToken,
	}
	status, err := client.waitForDecision(ctx, actionID)
	if err != nil {
		return false, nil, err
	}
	if strings.TrimSpace(status.Decision) == "deny" {
		fingerprint := strings.TrimSpace(status.CommandFingerprint)
		if fingerprint == "" {
			fingerprint = command.Fingerprint
			status.CommandFingerprint = fingerprint
		}
		s.rememberTaskDeniedApproval(req.TaskRunID, fingerprint, status)
		return false, &status, nil
	}
	return strings.TrimSpace(status.Decision) == "approve", &status, nil
}

func (s *Service) startDetachedCommandWatcher(req toolCallRequest, run backgroundCommandRun, execution *commandExecution) {
	go func() {
		defer s.releaseBackgroundCommand(run.CommandRunID)
		defer s.releaseRunningCommand(run.CommandRunID)
		result := <-execution.resultCh
		finishCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.postCommandFinishedEvent(finishCtx, req, run.CommandRunID, run.Command, run.CWD, result, run.LogPath); err != nil {
			log.Printf("[MCP-SSE] detached command finished emit failed session=%s task=%s commandRun=%s err=%v",
				req.SessionID, req.TaskRunID, run.CommandRunID, err)
		}
	}()
}

func (s *Service) postCommandDetachedEvent(ctx context.Context, req toolCallRequest, run backgroundCommandRun, mode string, waitTimeoutSec int) error {
	err := s.serverClient.postEvent(ctx, daemonEvent{
		SessionID: req.SessionID,
		TaskRunID: req.TaskRunID,
		EventType: "command.detached",
		Payload: map[string]any{
			"commandRunId":   run.CommandRunID,
			"command":        run.Command,
			"cwd":            run.CWD,
			"pid":            run.PID,
			"logPath":        run.LogPath,
			"mode":           mode,
			"waitTimeoutSec": waitTimeoutSec,
		},
	})
	if err != nil {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, req.ToolName, "", run.CommandRunID, "command_detached_emit", "error", err)
		return err
	}
	logMCPSSEAudit(req.SessionID, req.TaskRunID, req.ToolName, "", run.CommandRunID, "command_detached_emit", "ok", nil)
	return nil
}

func (s *Service) postCommandFinishedEvent(
	ctx context.Context,
	req toolCallRequest,
	commandRunID string,
	rawCommand string,
	execCWD string,
	result commandExecutionResult,
	logPath string,
) error {
	payload := map[string]any{
		"commandRunId":     commandRunID,
		"command":          rawCommand,
		"cwd":              execCWD,
		"status":           result.Status,
		"exitCode":         result.ExitCode,
		"aggregatedOutput": result.Output,
		"durationMs":       result.DurationMs,
	}
	if strings.TrimSpace(result.DenyType) != "" {
		payload["denyType"] = strings.TrimSpace(result.DenyType)
	}
	if logPath != "" {
		payload["logPath"] = logPath
	}
	if result.OutputTruncated {
		payload["outputTruncated"] = true
	}
	err := s.serverClient.postEvent(ctx, daemonEvent{
		SessionID: req.SessionID,
		TaskRunID: req.TaskRunID,
		EventType: "command.finished",
		Payload:   payload,
	})
	if err != nil {
		logMCPSSEAudit(req.SessionID, req.TaskRunID, req.ToolName, "", commandRunID, "command_finished_emit", "error", err)
		return err
	}
	logMCPSSEAudit(req.SessionID, req.TaskRunID, req.ToolName, "", commandRunID, "command_finished_emit", result.Status, nil)
	return nil
}

func backgroundCommandToolResult(run backgroundCommandRun, mode string, waitTimeoutSec int, detachedAfterSec int) map[string]any {
	text := fmt.Sprintf("command detached to background (commandRunId=%s pid=%d logPath=%s)", run.CommandRunID, run.PID, run.LogPath)
	if detachedAfterSec > 0 {
		text = fmt.Sprintf("%s after waiting %ds", text, detachedAfterSec)
	}
	return map[string]any{
		"content":        []map[string]any{{"type": "text", "text": text}},
		"mode":           mode,
		"detached":       true,
		"commandRunId":   run.CommandRunID,
		"pid":            run.PID,
		"logPath":        run.LogPath,
		"waitTimeoutSec": waitTimeoutSec,
	}
}

func appendCommandResultMessage(output string, message string) string {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return output
	}
	base := strings.TrimSpace(output)
	if base == "" {
		return msg
	}
	return base + "\n\n" + msg
}

func truncateForLog(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if max <= 0 || len(trimmed) <= max {
		return trimmed
	}
	return trimmed[:max] + "...(truncated)"
}

func resultIsError(result map[string]any) bool {
	value, ok := result["isError"]
	if !ok {
		return false
	}
	isError, ok := value.(bool)
	return ok && isError
}

func logMCPSSEAudit(sessionID, taskRunID, toolName, actionID, commandRunID, phase, status string, err error) {
	errText := "-"
	if err != nil {
		errText = truncateForLog(err.Error(), 280)
	}
	log.Printf("%s session=%s task=%s tool=%s actionId=%s commandRunId=%s phase=%s status=%s err=%q",
		mcpSSEAuditPrefix,
		auditField(sessionID),
		auditField(taskRunID),
		auditField(toolName),
		auditField(actionID),
		auditField(commandRunID),
		auditField(phase),
		auditField(status),
		errText,
	)
}

func auditField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\t", "\\t")
	return value
}

func (s *Service) resolveRunCommandExecCWD(req toolCallRequest, normalizedCWD string) string {
	workspaceRoot := strings.TrimSpace(s.getTaskWorkspace(req.TaskRunID))
	if workspaceRoot == "" {
		workspaceRoot = "."
	}
	if !filepath.IsAbs(workspaceRoot) {
		if absRoot, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = absRoot
		}
	}

	relativeCWD := safeRelativeCWD(normalizedCWD)
	if relativeCWD == "." {
		return filepath.Clean(workspaceRoot)
	}
	return filepath.Clean(filepath.Join(workspaceRoot, relativeCWD))
}

func (s *Service) executeWriteFileTool(ctx context.Context, req toolCallRequest) map[string]any {
	var args map[string]any
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "invalid arguments"}}, "isError": true}
	}

	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "Error: path is required"}}, "isError": true}
	}

	targetPath, err := s.resolveWriteFileTargetPath(req, path)
	if err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "Error: " + err.Error()}}, "isError": true}
	}

	dir := filepath.Dir(targetPath)
	if dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	// Determine kind based on whether the file existed before this write.
	existedBefore := false
	if _, statErr := os.Stat(targetPath); statErr == nil {
		existedBefore = true
	}

	if err := os.WriteFile(targetPath, []byte(content), 0644); err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "Error writing file: " + err.Error()}}, "isError": true}
	}

	if strings.TrimSpace(req.SessionID) != "" && strings.TrimSpace(req.TaskRunID) != "" {
		kind := "added"
		if existedBefore {
			kind = "modified"
		}
		dispatch := taskDispatch{
			SessionID:             req.SessionID,
			TaskRunID:             req.TaskRunID,
			WorkspaceRoot:         s.getTaskWorkspace(req.TaskRunID),
			WorkspaceSnapshotRoot: s.getTaskWorkspaceSnapshot(req.TaskRunID),
		}

		// Use the canonical helper that diffs current workspace state vs
		// turn snapshot. Same source of truth as the Codex fileChange path.
		emitCumulativeFileTouched(ctx, &s.serverClient, dispatch, targetPath, kind, "mcp", "create_file")
	}

	return map[string]any{"content": []map[string]any{{"type": "text", "text": "File written successfully: " + path}}}
}

func (s *Service) resolveWriteFileTargetPath(req toolCallRequest, rawPath string) (string, error) {
	requestPath := strings.TrimSpace(rawPath)
	if requestPath == "" {
		return "", fmt.Errorf("path is required")
	}

	workspaceRoot := strings.TrimSpace(s.getTaskWorkspace(req.TaskRunID))
	if workspaceRoot == "" {
		workspaceRoot = "."
	}
	if !filepath.IsAbs(workspaceRoot) {
		absRoot, err := filepath.Abs(workspaceRoot)
		if err != nil {
			return "", fmt.Errorf("resolve workspace root failed: %w", err)
		}
		workspaceRoot = absRoot
	}
	workspaceRoot = filepath.Clean(workspaceRoot)

	var targetPath string
	if filepath.IsAbs(requestPath) {
		targetPath = filepath.Clean(requestPath)
	} else {
		relativePath := filepath.Clean(requestPath)
		if relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path must stay within workspace root")
		}
		targetPath = filepath.Clean(filepath.Join(workspaceRoot, relativePath))
	}

	rel, err := filepath.Rel(workspaceRoot, targetPath)
	if err != nil {
		return "", fmt.Errorf("validate path failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must stay within workspace root")
	}

	return targetPath, nil
}
