package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

const (
	riskLevelSafeRead     = "safe_read"
	riskLevelGuardedWrite = "guarded_write"
	riskLevelDestructive  = "destructive"
)

type runCommandRequest struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	CWD        string   `json:"cwd"`
	Reason     string   `json:"reason"`
	TimeoutSec int      `json:"timeoutSec"`
	StdinText  string   `json:"stdinText,omitempty"`
}

type normalizedCommand struct {
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
	CWD         string   `json:"cwd"`
	Reason      string   `json:"reason"`
	TimeoutSec  int      `json:"timeoutSec"`
	Fingerprint string   `json:"fingerprint"`
	RiskLevel   string   `json:"riskLevel"`
}

func normalizeRunCommand(req runCommandRequest) (normalizedCommand, error) {
	executable := strings.TrimSpace(req.Executable)
	if executable == "" {
		return normalizedCommand{}, errors.New("executable is required")
	}
	if strings.Contains(executable, "/") || strings.Contains(executable, `\`) {
		return normalizedCommand{}, errors.New("executable must not contain path separators")
	}
	if isShellWrapper(executable) {
		return normalizedCommand{}, errors.New("shell wrapper executables are not allowed")
	}

	cwd := strings.TrimSpace(req.CWD)
	if cwd == "" {
		cwd = "."
	}
	cleanCWD := filepath.Clean(cwd)
	if filepath.IsAbs(cleanCWD) || cleanCWD == ".." || strings.HasPrefix(cleanCWD, ".."+string(filepath.Separator)) {
		return normalizedCommand{}, errors.New("cwd must stay within the workspace root")
	}

	timeoutSec := req.TimeoutSec
	if timeoutSec == 0 {
		timeoutSec = 60
	}
	if timeoutSec < 1 || timeoutSec > 300 {
		return normalizedCommand{}, errors.New("timeoutSec must be between 1 and 300")
	}

	args := append([]string(nil), req.Args...)
	fingerprint, err := commandFingerprint(executable, args, cleanCWD)
	if err != nil {
		return normalizedCommand{}, err
	}

	return normalizedCommand{
		Executable:  executable,
		Args:        args,
		CWD:         cleanCWD,
		Reason:      strings.TrimSpace(req.Reason),
		TimeoutSec:  timeoutSec,
		Fingerprint: fingerprint,
		RiskLevel:   classifyCommandRisk(executable, args),
	}, nil
}

func normalizeCommandText(commandText, cwd, reason string) normalizedCommand {
	commandText = strings.TrimSpace(commandText)
	commandText = unwrapShellWrapperCommandText(commandText)
	fields := splitShellWords(commandText)
	if len(fields) == 0 {
		return normalizedCommand{
			Executable:  commandText,
			Args:        nil,
			CWD:         safeRelativeCWD(cwd),
			Reason:      strings.TrimSpace(reason),
			TimeoutSec:  60,
			Fingerprint: fallbackFingerprint(commandText, cwd),
			RiskLevel:   riskLevelGuardedWrite,
		}
	}

	normalized := normalizedCommand{
		Executable: canonicalExecutableName(fields[0]),
		Args:       append([]string(nil), fields[1:]...),
		CWD:        safeRelativeCWD(cwd),
		Reason:     strings.TrimSpace(reason),
		TimeoutSec: 60,
	}
	if fingerprint, err := commandFingerprint(normalized.Executable, normalized.Args, normalized.CWD); err == nil {
		normalized.Fingerprint = fingerprint
	} else {
		normalized.Fingerprint = fallbackFingerprint(commandText, cwd)
	}
	normalized.RiskLevel = classifyCommandTextRisk(commandText, normalized.Executable, normalized.Args)
	return normalized
}

func unwrapShellWrapperCommandText(commandText string) string {
	trimmed := strings.TrimSpace(commandText)
	if trimmed == "" {
		return ""
	}
	fields := splitShellWords(trimmed)
	if len(fields) == 0 {
		return trimmed
	}
	script, ok := extractShellScript(canonicalExecutableName(fields[0]), fields[1:])
	if !ok {
		return trimmed
	}
	script = strings.TrimSpace(script)
	if script == "" {
		return trimmed
	}
	return script
}

func classifyCommandRisk(executable string, args []string) string {
	executable = canonicalExecutableName(executable)
	if script, ok := extractShellScript(executable, args); ok {
		return classifyShellScriptRisk(script)
	}
	if hasWritableRedirection(args) {
		return riskLevelGuardedWrite
	}

	switch executable {
	case "rm", "sudo", "chmod", "chown":
		return riskLevelDestructive
	case "git":
		return classifyGitRisk(args)
	case "cat":
		return classifyCatRisk(args)
	case "sed":
		return classifySedRisk(args)
	case "find":
		return classifyFindRisk(args)
	case "xargs":
		return classifyXargsRisk(args)
	}

	if safeReadExecutables[executable] {
		return riskLevelSafeRead
	}
	return riskLevelGuardedWrite
}

func classifyCommandTextRisk(commandText, executable string, args []string) string {
	if script, ok := extractShellScript(executable, args); ok {
		return classifyShellScriptRisk(script)
	}
	return classifyCommandRisk(executable, args)
}

func classifyGitRisk(args []string) string {
	if len(args) == 0 {
		return riskLevelGuardedWrite
	}
	switch args[0] {
	case "status", "diff", "show", "log", "rev-parse", "ls-files":
		return riskLevelSafeRead
	case "branch":
		if len(args) == 1 || (len(args) == 2 && args[1] == "--show-current") {
			return riskLevelSafeRead
		}
		return riskLevelGuardedWrite
	case "remote":
		if len(args) == 1 || (len(args) == 2 && args[1] == "-v") {
			return riskLevelSafeRead
		}
		return riskLevelGuardedWrite
	case "submodule":
		if len(args) == 2 && args[1] == "status" {
			return riskLevelSafeRead
		}
		return riskLevelGuardedWrite
	case "reset":
		for _, arg := range args[1:] {
			if arg == "--hard" {
				return riskLevelDestructive
			}
		}
		return riskLevelGuardedWrite
	case "clean":
		return riskLevelDestructive
	default:
		return riskLevelGuardedWrite
	}
}

func classifySedRisk(args []string) string {
	for _, arg := range args {
		if arg == "-i" || strings.HasPrefix(arg, "-i") {
			return riskLevelGuardedWrite
		}
	}
	return riskLevelSafeRead
}

func classifyFindRisk(args []string) string {
	for _, arg := range args {
		switch arg {
		case "-delete":
			return riskLevelDestructive
		case "-exec", "-execdir", "-ok", "-okdir":
			return riskLevelGuardedWrite
		}
	}
	return riskLevelSafeRead
}

func classifyXargsRisk(args []string) string {
	if len(args) == 0 {
		return riskLevelGuardedWrite
	}

	commandIdx := -1
	for idx, arg := range args {
		if strings.HasPrefix(arg, "-") {
			// Flags such as -r or -I keep xargs in orchestration mode.
			continue
		}
		commandIdx = idx
		break
	}
	if commandIdx == -1 {
		return riskLevelGuardedWrite
	}

	return classifyCommandRisk(args[commandIdx], args[commandIdx+1:])
}

func classifyCatRisk(args []string) string {
	if len(args) == 0 {
		return riskLevelGuardedWrite
	}
	hasReadableInput := false
	for idx := 0; idx < len(args); idx++ {
		arg := strings.TrimSpace(args[idx])
		if arg == "" {
			continue
		}
		if isRedirectionLikeToken(arg) {
			if isSafeDevNullRedirectToken(arg, idx, args) {
				if isRedirectionOperatorToken(arg) {
					idx++
				}
				continue
			}
			return riskLevelGuardedWrite
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		hasReadableInput = true
	}
	if !hasReadableInput {
		return riskLevelGuardedWrite
	}
	return riskLevelSafeRead
}

func shouldAutoApprove(cmd normalizedCommand) bool {
	return cmd.RiskLevel == riskLevelSafeRead
}

func allowsSessionApprovalForRisk(riskLevel string) bool {
	return riskLevel != riskLevelDestructive
}

func allowsCommandForProfile(profile turnExecutionProfile, cmd normalizedCommand) bool {
	if !profile.ReadOnly {
		return true
	}
	switch cmd.RiskLevel {
	case riskLevelSafeRead, riskLevelDestructive:
		return true
	default:
		return false
	}
}

func commandFingerprint(executable string, args []string, cwd string) (string, error) {
	payload := struct {
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
		CWD        string   `json:"cwd"`
	}{
		Executable: executable,
		Args:       args,
		CWD:        cwd,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func fallbackFingerprint(commandText, cwd string) string {
	sum := sha256.Sum256([]byte(commandText + "\n" + cwd))
	return hex.EncodeToString(sum[:])
}

func isShellWrapper(executable string) bool {
	switch canonicalExecutableName(executable) {
	case "bash", "sh", "zsh", "fish", "cmd.exe", "powershell", "pwsh":
		return true
	default:
		return false
	}
}

func canonicalExecutableName(executable string) string {
	trimmed := strings.TrimSpace(executable)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(trimmed)
	if base == "." || base == string(filepath.Separator) {
		return trimmed
	}
	return base
}

func extractShellScript(executable string, args []string) (string, bool) {
	if !isShellWrapper(executable) {
		return "", false
	}
	for idx := 0; idx < len(args); idx++ {
		switch args[idx] {
		case "-c", "-lc", "-cl":
			if idx+1 < len(args) {
				return strings.TrimSpace(args[idx+1]), true
			}
			return "", true
		}
	}
	return "", true
}

func classifyShellScriptRisk(script string) string {
	script = strings.TrimSpace(script)
	if script == "" {
		return riskLevelGuardedWrite
	}

	risk := riskLevelSafeRead
	for _, segment := range splitShellSegments(script) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		executable, args, ok := extractSegmentCommand(segment)
		if !ok {
			return riskLevelGuardedWrite
		}
		segmentRisk := classifyCommandRisk(executable, args)
		if segmentRisk == riskLevelDestructive {
			return riskLevelDestructive
		}
		if segmentRisk == riskLevelGuardedWrite {
			risk = riskLevelGuardedWrite
		}
	}
	return risk
}

func splitShellSegments(script string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	return strings.Split(replacer.Replace(script), "\n")
}

func splitShellWords(input string) []string {
	var fields []string
	var current strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		fields = append(fields, current.String())
		current.Reset()
	}

	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		switch {
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return fields
}

func extractSegmentCommand(segment string) (string, []string, bool) {
	fields := splitShellWords(segment)
	if len(fields) == 0 {
		return "", nil, false
	}

	filtered := make([]string, 0, len(fields))
	for idx := 0; idx < len(fields); idx++ {
		field := fields[idx]
		if strings.TrimSpace(field) == "" {
			continue
		}
		if isRedirectionOperatorToken(field) {
			if isSafeDevNullRedirectToken(field, idx, fields) {
				idx++
				continue
			}
			return "", nil, false
		}
		if isRedirectionLikeToken(field) {
			if !isSafeDevNullRedirectToken(field, idx, fields) {
				return "", nil, false
			}
			continue
		}
		if field == "2>/dev/null" || field == "1>/dev/null" || field == ">/dev/null" {
			continue
		}
		filtered = append(filtered, field)
	}

	if len(filtered) == 0 {
		return "", nil, false
	}
	return canonicalExecutableName(filtered[0]), filtered[1:], true
}

func hasWritableRedirection(args []string) bool {
	for idx := 0; idx < len(args); idx++ {
		arg := strings.TrimSpace(args[idx])
		if arg == "" {
			continue
		}
		if !isRedirectionLikeToken(arg) {
			continue
		}
		if isSafeDevNullRedirectToken(arg, idx, args) {
			if isRedirectionOperatorToken(arg) {
				idx++
			}
			continue
		}
		return true
	}
	return false
}

func isRedirectionLikeToken(token string) bool {
	if isRedirectionOperatorToken(token) {
		return true
	}
	return strings.HasPrefix(token, ">") ||
		strings.HasPrefix(token, "<") ||
		strings.HasPrefix(token, "1>") ||
		strings.HasPrefix(token, "2>")
}

func isSafeDevNullRedirectToken(token string, idx int, fields []string) bool {
	if token == ">" || token == "1>" || token == "2>" || token == "<" {
		if idx+1 >= len(fields) {
			return false
		}
		return fields[idx+1] == "/dev/null"
	}
	if strings.HasPrefix(token, "2>/dev/null") || strings.HasPrefix(token, "1>/dev/null") || strings.HasPrefix(token, ">/dev/null") {
		return true
	}
	return false
}

func isRedirectionOperatorToken(token string) bool {
	switch token {
	case ">", ">>", "<", "<<", "1>", "1>>", "2>", "2>>":
		return true
	default:
		return false
	}
}

func safeRelativeCWD(cwd string) string {
	cleanCWD := filepath.Clean(strings.TrimSpace(cwd))
	if cleanCWD == "" || cleanCWD == "." {
		return "."
	}
	if filepath.IsAbs(cleanCWD) {
		return "."
	}
	if cleanCWD == ".." || strings.HasPrefix(cleanCWD, ".."+string(filepath.Separator)) {
		return "."
	}
	return cleanCWD
}

var safeReadExecutables = map[string]bool{
	"pwd":      true,
	"ls":       true,
	"head":     true,
	"tail":     true,
	"wc":       true,
	"du":       true,
	"df":       true,
	"find":     true,
	"grep":     true,
	"rg":       true,
	"sed":      true,
	"sort":     true,
	"stat":     true,
	"file":     true,
	"tree":     true,
	"which":    true,
	"realpath": true,
	"readlink": true,
}
