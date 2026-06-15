package app

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestIsSelfUpdateTurnPromptPositive(t *testing.T) {
	// The body produced by CliUpdateBanner::buildTurnBody — keep this fixture
	// close to the real shape so any change to the banner wording surfaces
	// here as well as in the contract test.
	prompt := strings.Join([]string{
		"[CLI self-update approved by user]",
		"",
		"Please run exactly the following shell command, without modification, additional confirmation, or commentary:",
		"",
		"    npm install -g @openai/codex@latest",
		"",
		"When it completes, run `codex --version` and report the new version. If the command fails, report the full error output verbatim.",
	}, "\n")
	if !isSelfUpdateTurnPrompt(prompt) {
		t.Fatalf("expected canonical banner prompt to be detected as self-update")
	}
}

func TestIsSelfUpdateTurnPromptLeadingWhitespaceStillMatches(t *testing.T) {
	// Defensive: if a future banner revision adds a leading newline or two
	// spaces of indentation, the hook must still fire. The TrimSpace inside
	// isSelfUpdateTurnPrompt exists precisely for this.
	prompt := "\n  \t" + cliSelfUpdateMarker + "\n\nbody"
	if !isSelfUpdateTurnPrompt(prompt) {
		t.Fatalf("expected leading whitespace to be ignored")
	}
}

func TestIsSelfUpdateTurnPromptNegative(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
	}{
		{"empty", ""},
		{"whitespace only", "   \n\t  "},
		{"normal user turn", "Please refactor foo.go"},
		// The marker appears mid-prompt but is not at the start — must not
		// match, otherwise a user quoting our marker in a comment would
		// accidentally trigger a capability refresh.
		{"marker in middle", "I saw a banner saying [CLI self-update approved by user]"},
		// Close but not identical: capitalised differently.
		{"case mismatch", "[cli self-update approved by user]"},
		// Close but missing the brackets.
		{"missing brackets", "CLI self-update approved by user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isSelfUpdateTurnPrompt(tc.prompt) {
				t.Fatalf("expected prompt %q to NOT match self-update marker", tc.prompt)
			}
		})
	}
}

// TestSelfUpdateMarkerMatchesWeb is a cross-language contract test: it
// reads the web banner source file and asserts that the literal exported
// as SELF_UPDATE_MARKER equals the Go constant cliSelfUpdateMarker.
//
// If you change one, you must change the other — otherwise the daemon
// will stop recognising self-update turns and the UX optimisation silently
// degrades to "wait for capabilityTicker".
//
// Locating the web file is best-effort: the daemon source lives under
// either `cwp-agent` (standalone checkout) or `PocketCode/daemon`
// (symlinked into the meta-repo). We try both relative layouts and an
// explicit POCKETCODE_WEB_BANNER_PATH override before skipping.
func TestSelfUpdateMarkerMatchesWeb(t *testing.T) {
	webFile := locateWebBannerFile()
	if webFile == "" {
		t.Skip("web banner file not found in any expected location; " +
			"set POCKETCODE_WEB_BANNER_PATH or run from a checkout that includes web/app/cli-update-banner.tsx")
	}
	data, err := os.ReadFile(webFile)
	if err != nil {
		t.Fatalf("read web banner file %s: %v", webFile, err)
	}

	// Match: export const SELF_UPDATE_MARKER = "..."
	// Tolerates whitespace and an optional type annotation.
	re := regexp.MustCompile(`SELF_UPDATE_MARKER\s*(?::\s*string)?\s*=\s*"([^"\\]*)"`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("could not locate SELF_UPDATE_MARKER constant in %s; "+
			"if the symbol was renamed, update this contract test accordingly", webFile)
	}
	if m[1] != cliSelfUpdateMarker {
		t.Fatalf("self-update marker drift between web and daemon:\n  web:    %q\n  daemon: %q\n"+
			"update either web/app/cli-update-banner.tsx::SELF_UPDATE_MARKER "+
			"or daemon/internal/app/cli_self_update.go::cliSelfUpdateMarker "+
			"so the two match.", m[1], cliSelfUpdateMarker)
	}
}

// locateWebBannerFile returns the absolute path to the web banner source if
// it can be found, or empty string if not. It checks (in order) the env
// override, the layout used when daemon is symlinked into PocketCode, and
// the layout used in a sibling-checkout setup.
func locateWebBannerFile() string {
	if env := strings.TrimSpace(os.Getenv("POCKETCODE_WEB_BANNER_PATH")); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	thisDir := filepath.Dir(thisFile)
	// thisDir = <root>/internal/app
	// Candidates:
	//   - <root>/../web/app/cli-update-banner.tsx          (PocketCode/daemon → PocketCode/web)
	//   - <root>/../../PocketCode/web/app/cli-update-banner.tsx (cwp-agent sibling to PocketCode)
	candidates := []string{
		filepath.Join(thisDir, "..", "..", "..", "web", "app", "cli-update-banner.tsx"),
		filepath.Join(thisDir, "..", "..", "..", "..", "PocketCode", "web", "app", "cli-update-banner.tsx"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
