package app

import "strings"

const (
	commandDenyTypePolicy = "policy_denied"
	commandDenyTypeUser   = "user_denied"
	commandDenyTypeSystem = "system_failed"
)

func commandDenyTypeFromApproval(status *approvalStatus, err error) string {
	if err != nil {
		return commandDenyTypeSystem
	}
	if status != nil && strings.TrimSpace(status.Decision) == "deny" {
		return commandDenyTypeUser
	}
	return ""
}
