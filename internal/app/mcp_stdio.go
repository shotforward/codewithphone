package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

func RunMCPStdio() {
	baseURL := os.Getenv("POCKETCODE_MCP_DAEMON_URL")
	if baseURL == "" {
		log.Fatal("POCKETCODE_MCP_DAEMON_URL is required")
	}
	sessionID := os.Getenv("POCKETCODE_MCP_SESSION_ID")
	if sessionID == "" {
		log.Fatal("POCKETCODE_MCP_SESSION_ID is required")
	}
	taskRunID := os.Getenv("POCKETCODE_MCP_TASK_RUN_ID")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		if req.Method == "initialize" {
			sendMCPResp(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "pocketcode",
					"version": "1.0.0",
				},
			})
		} else if req.Method == "tools/list" {
			sendMCPResp(req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "run_command",
						"description": "Execute a shell command. Use this tool ONLY. You MUST use this tool to run shell commands.",
						"inputSchema": map[string]any{
							"type": "object",
							"required": []string{
								"command",
							},
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
							"type": "object",
							"required": []string{
								"path", "content",
							},
							"properties": map[string]any{
								"path":    map[string]any{"type": "string"},
								"content": map[string]any{"type": "string"},
							},
						},
					},
				},
			})
		} else if req.Method == "tools/call" {
			// forward to daemon over HTTP!
			handleToolCall(baseURL, sessionID, taskRunID, req)
		} else if req.Method == "notifications/initialized" {
			// ignore
		}
	}
}

func sendMCPResp(id json.RawMessage, result any) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	b, _ := json.Marshal(resp)
	fmt.Printf("%s\n", b)
}

func sendMCPError(id json.RawMessage, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]any{
			"code":    code,
			"message": message,
		},
	}
	b, _ := json.Marshal(resp)
	fmt.Printf("%s\n", b)
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func handleToolCall(baseURL, sessionID, taskRunID string, req jsonRPCRequest) {
	var params mcpToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		sendMCPError(req.ID, -32602, "invalid params")
		return
	}

	payload := map[string]any{
		"session_id":  sessionID,
		"task_run_id": taskRunID,
		"tool_name":   params.Name,
		"arguments":   params.Arguments,
	}
	b, _ := json.Marshal(payload)
	httpReq, err := http.NewRequest("POST", baseURL+"/internal/mcp/tool_call", bytes.NewReader(b))
	if err != nil {
		sendMCPError(req.ID, -32000, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		sendMCPError(req.ID, -32000, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		sendMCPError(req.ID, -32000, string(body))
		return
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		sendMCPError(req.ID, -32000, err.Error())
		return
	}
	sendMCPResp(req.ID, result)
}
