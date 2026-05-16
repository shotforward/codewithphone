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

	// PolicyDecision is the result of evaluating the command against the
	// allow / deny ruleset (see cmd_policy_engine.go). It drives both the
	// auto-approve gate and the deny-reason shown on the command card.
	PolicyDecision commandPolicyDecision `json:"-"`
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

	decision := evaluatePolicySingle(executable, args)
	return normalizedCommand{
		Executable:     executable,
		Args:           args,
		CWD:            cleanCWD,
		Reason:         strings.TrimSpace(req.Reason),
		TimeoutSec:     timeoutSec,
		Fingerprint:    fingerprint,
		RiskLevel:      riskLevelForCategory(decision.Category),
		PolicyDecision: decision,
	}, nil
}

func normalizeCommandText(commandText, cwd, reason string) normalizedCommand {
	rawCommand := strings.TrimSpace(commandText)
	unwrapped := unwrapShellWrapperCommandText(rawCommand)
	decision := evaluatePolicyScript(unwrapped)

	fields := splitShellWords(unwrapped)
	if len(fields) == 0 {
		return normalizedCommand{
			Executable:     unwrapped,
			Args:           nil,
			CWD:            safeRelativeCWD(cwd),
			Reason:         strings.TrimSpace(reason),
			TimeoutSec:     60,
			Fingerprint:    fallbackFingerprint(unwrapped, cwd),
			RiskLevel:      riskLevelForCategory(decision.Category),
			PolicyDecision: decision,
		}
	}

	normalized := normalizedCommand{
		Executable:     canonicalExecutableName(fields[0]),
		Args:           append([]string(nil), fields[1:]...),
		CWD:            safeRelativeCWD(cwd),
		Reason:         strings.TrimSpace(reason),
		TimeoutSec:     60,
		PolicyDecision: decision,
		RiskLevel:      riskLevelForCategory(decision.Category),
	}
	if fingerprint, err := commandFingerprint(normalized.Executable, normalized.Args, normalized.CWD); err == nil {
		normalized.Fingerprint = fingerprint
	} else {
		normalized.Fingerprint = fallbackFingerprint(unwrapped, cwd)
	}
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

// classifyCommandRisk is a back-compat wrapper around the new policy engine.
// New callers should use evaluatePolicy / evaluatePolicySingle directly and
// look at the full commandPolicyDecision.
func classifyCommandRisk(executable string, args []string) string {
	if script, ok := extractShellScript(executable, args); ok {
		return riskLevelForCategory(evaluatePolicyScript(script).Category)
	}
	return riskLevelForCategory(evaluatePolicySingle(executable, args).Category)
}

func shouldAutoApprove(cmd normalizedCommand) bool {
	if cmd.PolicyDecision.Category != "" {
		return cmd.PolicyDecision.Allow
	}
	// Fallback path for any normalizedCommand built without going through
	// the engine — defer to the legacy risk-level heuristic.
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

