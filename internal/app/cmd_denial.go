package app

import "strings"

const (
	commandDenyTypePolicy = "policy_denied"
	commandDenyTypeUser   = "user_denied"
	commandDenyTypeSystem = "system_failed"

	// Policy-deny subtypes — surfaced on command.finished events so the
	// approval card can show a specific reason for the rejection.
	commandDenyTypeInlineScript    = "policy_denied:inline_script"
	commandDenyTypePrivilege       = "policy_denied:privilege"
	commandDenyTypeSystemPkgMgr    = "policy_denied:system_pkg_mgr"
	commandDenyTypeDangerousFS     = "policy_denied:dangerous_fs"
	commandDenyTypeDestructiveGit  = "policy_denied:destructive_git"
	commandDenyTypePublish         = "policy_denied:publish"
	commandDenyTypeCloudCLI        = "policy_denied:cloud_cli"
	commandDenyTypeRemoteShell     = "policy_denied:remote_shell"
	commandDenyTypeSystemService   = "policy_denied:system_service"
	commandDenyTypeContainerEscape = "policy_denied:container_escape"
	commandDenyTypeNetworkPipe     = "policy_denied:network_pipe"
	commandDenyTypeCredential      = "policy_denied:credential"
	commandDenyTypeWorkspaceEscape = "policy_denied:workspace_escape"
	commandDenyTypeProcessKill     = "policy_denied:process_kill"
	commandDenyTypeGlobUnsafe      = "policy_denied:glob_unsafe"
	commandDenyTypeUnparseable     = "policy_denied:unparseable"
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
