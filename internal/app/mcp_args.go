package app

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	runCommandExecutionModeWait       = "wait"
	runCommandExecutionModeAuto       = "auto"
	runCommandExecutionModeBackground = "background"

	defaultAutoWaitTimeoutSec = 120
	minWaitTimeoutSec         = 1
	maxWaitTimeoutSec         = 1800
)

type parsedRunCommandToolArgs struct {
	RawCommand     string
	CWD            string
	Reason         string
	CommandSource  string
	Keys           []string
	ExecutionMode  string
	WaitTimeoutSec int
}

func parseRunCommandToolArgs(raw json.RawMessage) (parsedRunCommandToolArgs, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err == nil && args != nil {
		return parseRunCommandToolArgsMap(args)
	}

	var commandText string
	if err := json.Unmarshal(raw, &commandText); err == nil {
		commandText = strings.TrimSpace(commandText)
		if commandText == "" {
			return parsedRunCommandToolArgs{}, fmt.Errorf("command is required")
		}
		return parsedRunCommandToolArgs{
			RawCommand:    commandText,
			CWD:           ".",
			CommandSource: "string_argument",
			ExecutionMode: runCommandExecutionModeWait,
		}, nil
	}

	return parsedRunCommandToolArgs{}, fmt.Errorf("arguments must be a JSON object or command string")
}

func parseRunCommandToolArgsMap(args map[string]any) (parsedRunCommandToolArgs, error) {
	command, source := extractCommandText(args)
	if command == "" {
		return parsedRunCommandToolArgs{}, fmt.Errorf("command is required (supported: command/cmd/shell_command/executable+args)")
	}
	command = unwrapShellWrapperCommandText(command)

	cwd := firstNonEmptyString(args,
		"cwd", "workdir", "working_directory", "workingDirectory", "directory", "path")
	if cwd == "" {
		cwd = "."
	}
	reason := firstNonEmptyString(args, "reason", "justification", "why")

	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	executionMode := parseExecutionMode(args)
	waitTimeoutSec := parseWaitTimeoutSec(args, executionMode)

	return parsedRunCommandToolArgs{
		RawCommand:     command,
		CWD:            cwd,
		Reason:         reason,
		CommandSource:  source,
		Keys:           keys,
		ExecutionMode:  executionMode,
		WaitTimeoutSec: waitTimeoutSec,
	}, nil
}

func parseExecutionMode(args map[string]any) string {
	if truthy(args["background"]) || truthy(args["detach"]) || truthy(args["detached"]) {
		return runCommandExecutionModeBackground
	}

	mode := firstNonEmptyString(args,
		"executionMode", "execution_mode", "mode", "runMode", "run_mode",
	)
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "":
		return runCommandExecutionModeWait
	case runCommandExecutionModeWait, "sync", "foreground", "blocking":
		return runCommandExecutionModeWait
	case runCommandExecutionModeAuto:
		return runCommandExecutionModeAuto
	case runCommandExecutionModeBackground, "async", "detached", "daemon":
		return runCommandExecutionModeBackground
	default:
		return runCommandExecutionModeWait
	}
}

func parseWaitTimeoutSec(args map[string]any, mode string) int {
	for _, key := range []string{
		"waitTimeoutSec", "wait_timeout_sec", "wait_timeout",
		"timeoutSec", "timeout_sec", "timeout",
	} {
		value, ok := extractIntValue(args[key])
		if !ok {
			continue
		}
		return clampWaitTimeout(value)
	}

	if mode == runCommandExecutionModeAuto {
		return defaultAutoWaitTimeoutSec
	}
	return 0
}

func clampWaitTimeout(value int) int {
	if value < minWaitTimeoutSec {
		return minWaitTimeoutSec
	}
	if value > maxWaitTimeoutSec {
		return maxWaitTimeoutSec
	}
	return value
}

func extractCommandText(args map[string]any) (string, string) {
	for _, key := range []string{"command", "cmd", "shell_command", "shellCommand", "raw_command", "rawCommand"} {
		if value, ok := extractStringValue(args[key]); ok {
			return value, key
		}
		if parts, ok := extractStringList(args[key]); ok && len(parts) > 0 {
			return joinAsShellCommand(parts), key + "[]"
		}
	}

	if commandObj, ok := args["command"].(map[string]any); ok {
		command, source := extractCommandFromObject(commandObj)
		if command != "" {
			return command, "command." + source
		}
	}

	command, source := extractCommandFromObject(args)
	if command != "" {
		return command, source
	}

	return "", ""
}

func extractCommandFromObject(args map[string]any) (string, string) {
	executable := firstNonEmptyString(args, "executable", "program", "binary")
	if executable == "" {
		return "", ""
	}

	argvKeys := []string{"args", "argv", "arguments"}
	parts := []string{executable}
	for _, key := range argvKeys {
		if argv, ok := extractStringList(args[key]); ok {
			parts = append(parts, argv...)
			break
		}
	}

	return joinAsShellCommand(parts), "executable+args"
}

func extractStringValue(raw any) (string, bool) {
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func extractStringList(raw any) ([]string, bool) {
	values, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return parts, true
}

func extractIntValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float32:
		return int(math.Round(float64(value))), true
	case float64:
		return int(math.Round(value)), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func truthy(raw any) bool {
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func firstNonEmptyString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := extractStringValue(values[key]); ok {
			return value
		}
	}
	return ""
}

func joinAsShellCommand(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		escaped = append(escaped, shellQuotePart(part))
	}
	return strings.Join(escaped, " ")
}

func shellQuotePart(part string) string {
	if part == "" {
		return "''"
	}
	if isShellSafe(part) {
		return part
	}
	return "'" + strings.ReplaceAll(part, "'", "'\"'\"'") + "'"
}

func isShellSafe(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		switch char {
		case '_', '-', '.', '/', ':', '=', '@', ',', '+', '%':
			continue
		default:
			return false
		}
	}
	return true
}
