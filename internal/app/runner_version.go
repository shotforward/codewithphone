package app

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// runnerVersionConstraint defines the supported version range for a CLI tool.
// MinVersion is the oldest version we've tested against and know works.
// MaxTestedVersion is the newest version we've verified — anything newer
// triggers a warning but is NOT blocked (to avoid breaking users who
// upgrade their CLI before we update the constraint).
type runnerVersionConstraint struct {
	Name             string // human-readable: "Codex CLI", "Claude Code CLI", "Gemini CLI"
	Binary           string // path or command name: "codex", "claude", "gemini"
	MinVersion       string // oldest supported, e.g. "0.100.0"
	MaxTestedVersion string // newest verified, e.g. "0.118.0"
}

// knownRunnerConstraints returns the version constraints for all supported
// runners. Update these when a new CLI version is tested and confirmed
// working. See docs/runner-compatibility.md for the full compatibility
// matrix and changelog notes.
func knownRunnerConstraints() []runnerVersionConstraint {
	return []runnerVersionConstraint{
		{
			Name:             "Codex CLI",
			Binary:           "codex",
			MinVersion:       "0.100.0",
			MaxTestedVersion: "0.118.0",
		},
		{
			Name:             "Claude Code CLI",
			Binary:           "claude",
			MinVersion:       "2.0.0",
			MaxTestedVersion: "2.1.84",
		},
		{
			Name:             "Gemini CLI",
			Binary:           "gemini",
			MinVersion:       "0.30.0",
			MaxTestedVersion: "0.37.0",
		},
	}
}

// detectCLIVersion runs `<binary> --version` and returns the trimmed version
// string. Returns ("", err) if the binary is missing or doesn't support
// --version.
func detectCLIVersion(binary string) (string, error) {
	cmd := exec.Command(binary, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", binary, err)
	}
	// Different CLIs format version differently:
	//   codex:  "codex-cli 0.118.0"
	//   claude: "2.1.84 (Claude Code)"
	//   gemini: "0.37.0"
	// Extract just the version number (first token that looks like semver).
	raw := strings.TrimSpace(string(out))
	return extractVersionNumber(raw), nil
}

// extractVersionNumber pulls the first semver-like token from a version
// string. Handles "codex-cli 0.118.0", "2.1.84 (Claude Code)", "0.37.0".
func extractVersionNumber(raw string) string {
	for _, token := range strings.Fields(raw) {
		cleaned := strings.TrimRight(token, ",;)")
		cleaned = strings.TrimLeft(cleaned, "(")
		parts := strings.Split(cleaned, ".")
		if len(parts) >= 2 && isDigits(parts[0]) {
			return cleaned
		}
	}
	return strings.TrimSpace(raw)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// compareVersions does a simple semver-like comparison.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
// Only compares numeric dot-separated components; ignores pre-release tags.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var va, vb int
		if i < len(partsA) {
			fmt.Sscanf(partsA[i], "%d", &va)
		}
		if i < len(partsB) {
			fmt.Sscanf(partsB[i], "%d", &vb)
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}

// CheckRunnerVersions detects and logs the version of each CLI tool.
// It does NOT block startup — just logs warnings for out-of-range versions
// so operators can see at a glance if an upgrade is needed.
func CheckRunnerVersions(binOverrides map[string]string) {
	constraints := knownRunnerConstraints()
	for _, c := range constraints {
		binary := c.Binary
		if override, ok := binOverrides[c.Binary]; ok && override != "" {
			binary = override
		}
		version, err := detectCLIVersion(binary)
		if err != nil {
			log.Printf("[VERSION] %s (%s): not found or --version failed: %v", c.Name, binary, err)
			continue
		}
		if compareVersions(version, c.MinVersion) < 0 {
			log.Printf("[VERSION] ⚠ %s %s is BELOW minimum supported %s — some features may not work", c.Name, version, c.MinVersion)
		} else if compareVersions(version, c.MaxTestedVersion) > 0 {
			log.Printf("[VERSION] ℹ %s %s is ABOVE max tested %s — should work but watch for unhandled events", c.Name, version, c.MaxTestedVersion)
		} else {
			log.Printf("[VERSION] ✓ %s %s (supported range: %s – %s)", c.Name, version, c.MinVersion, c.MaxTestedVersion)
		}
	}
}
