package app

import "strings"

// cliSelfUpdateMarker is the literal prefix that the web CliUpdateBanner
// places at the start of self-update turn prompts. The web client constructs
// this string in web/app/cli-update-banner.tsx where it is exported as
// SELF_UPDATE_MARKER.
//
// The daemon's runners look for this prefix on the dispatched prompt; when
// it matches, they trigger an immediate capability refresh after the turn
// finishes (instead of waiting up to capabilityTicker, ~10m) so the UI sees
// the new version and clears the banner within seconds.
//
// IMPORTANT: this constant must stay in lockstep with the web banner. The
// contract test TestSelfUpdateMarkerMatchesWeb in cli_self_update_test.go
// reads the web source file and fails if the two strings drift apart.
const cliSelfUpdateMarker = "[CLI self-update approved by user]"

// isSelfUpdateTurnPrompt reports whether the dispatched prompt was produced
// by the CliUpdateBanner approve flow.
//
// The marker is matched against the trimmed prompt prefix, so future banner
// changes that introduce leading whitespace or newlines do not silently
// break detection.
func isSelfUpdateTurnPrompt(prompt string) bool {
	return strings.HasPrefix(strings.TrimSpace(prompt), cliSelfUpdateMarker)
}
