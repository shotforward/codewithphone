package app

// Policy categories — used for logging and for surfacing the reason on the
// command approval card. Allow categories cause the command to auto-execute;
// deny categories cause a user approval prompt.
const (
	policyCategoryAllowRead           = "allow_read"
	policyCategoryAllowBuildTest      = "allow_build_test"
	policyCategoryAllowWorkspaceFile  = "allow_workspace_file"
	policyCategoryAllowLocalGit       = "allow_local_git"
	policyCategoryAllowGitFetch       = "allow_git_fetch"
	policyCategoryAllowPackageInstall = "allow_package_install"
	policyCategoryAllowCloudReadOnly  = "allow_cloud_readonly"
	policyCategoryAllowDefault        = "allow_default"

	policyCategoryDenyInlineScript    = "deny_inline_script"
	policyCategoryDenyPrivilege       = "deny_privilege"
	policyCategoryDenyPackageManager  = "deny_system_pkg_mgr"
	policyCategoryDenyDangerousFS     = "deny_dangerous_fs"
	policyCategoryDenyDestructiveGit  = "deny_destructive_git"
	policyCategoryDenyPublish         = "deny_publish"
	policyCategoryDenyCloudCLI        = "deny_cloud_cli"
	policyCategoryDenyRemoteShell     = "deny_remote_shell"
	policyCategoryDenySystemService   = "deny_system_service"
	policyCategoryDenyContainerEscape = "deny_container_escape"
	policyCategoryDenyNetworkPipe     = "deny_network_pipe"
	policyCategoryDenyCredential      = "deny_credential"
	policyCategoryDenyWorkspaceEscape = "deny_workspace_escape"
	policyCategoryDenyProcessKill     = "deny_process_kill"
	policyCategoryDenyGlobUnsafe      = "deny_glob_unsafe"
	policyCategoryDenyUnparseable     = "deny_unparseable"
)

// Allow list — read-only inspection utilities. These auto-execute and are
// safe to run from a read-only profile. Path operand checks still apply.
var allowReadExecutables = map[string]bool{
	"ls":        true,
	"ll":        true,
	"dir":       true,
	"tree":      true,
	"pwd":       true,
	"whoami":    true,
	"hostname":  true,
	"uname":     true,
	"id":        true,
	"groups":    true,
	"date":      true,
	"uptime":    true,
	"which":     true,
	"whereis":   true,
	"type":      true,
	"command":   true, // `command -v X`
	"cat":       true,
	"head":      true,
	"tail":      true,
	"less":      true,
	"more":      true,
	"bat":       true,
	"file":      true,
	"stat":      true,
	"wc":        true,
	"du":        true,
	"df":        true,
	"env":       true,
	"printenv":  true,
	"grep":      true,
	"egrep":     true,
	"fgrep":     true,
	"rg":        true,
	"ag":        true,
	"ack":       true,
	"fzf":       true,
	"locate":    true,
	"realpath":  true,
	"readlink":  true,
	"echo":      true,
	"printf":    true,
	"true":      true,
	"false":     true,
	"sort":      true,
	"uniq":      true,
	"awk":       true,
	"cut":       true,
	"tr":        true,
	"basename":  true,
	"dirname":   true,
	"jq":        true,
	"yq":        true,
	"xmllint":   true,
	"diff":      true,
	"cmp":       true,
	"column":    true,
	"comm":      true,
	"tee":       true, // tee writes, but commonly used in inspect pipelines; subcommand check below treats path arg
	"strings":   true,
	"od":        true,
	"hexdump":   true,
	"xxd":       true,
	"timeout":   true, // wrapper — body re-evaluated separately
}

// Allow list — build / test / formatter / language tooling. These run in
// workspace and may write workspace-internal files (caches, build output);
// they're auto-allowed in writable profiles.
var allowBuildTestExecutables = map[string]bool{
	"tsc":        true,
	"eslint":     true,
	"prettier":   true,
	"mypy":       true,
	"ruff":       true,
	"black":      true,
	"pylint":     true,
	"pytest":     true,
	"tox":        true,
	"gofmt":      true,
	"goimports":  true,
	"golangci-lint": true,
	"rustc":      true,
	"clippy":     true,
	"tsx":        true,
	"vitest":     true,
	"jest":       true,
	"bazel":      true,
	"ninja":      true,
	"meson":      true,
	"cmake":      true,
	"gradle":     true,
	"./gradlew":  true,
	"mvn":        true,
	"swift":      true,
	"flutter":    true,
	"dart":       true,
}

// Package managers / runtimes whose subcommand needs further inspection.
// (go / cargo / docker / git / make / npm / pnpm / yarn / bun / pip / kubectl
// / terraform / helm etc are handled in dedicated evaluators below.)

// Allow list — workspace-internal file operations. Path operand checks
// will catch out-of-workspace targets.
var allowWorkspaceFileExecutables = map[string]bool{
	"mkdir":  true,
	"touch":  true,
	"mv":     true, // operand-path check enforces workspace containment
	"cp":     true,
	"ln":     true,
}

// Always-deny executables — no subcommand or arg can save them.
var denyPrivilegeExecutables = map[string]bool{
	"sudo":   true,
	"su":     true,
	"doas":   true,
	"pkexec": true,
}

var denySystemPackageManagers = map[string]bool{
	"apt":     true,
	"apt-get": true,
	"yum":     true,
	"dnf":     true,
	"pacman":  true,
	"zypper":  true,
	"apk":     true,
	"port":    true,
}

var denyRemoteShellExecutables = map[string]bool{
	"ssh":     true,
	"scp":     true,
	"sftp":    true,
	"ftp":     true,
	"telnet":  true,
}

var denySystemServiceExecutables = map[string]bool{
	"systemctl":  true,
	"service":    true,
	"launchctl":  true,
	"initctl":    true,
	"rc-service": true,
}

var denyDestructiveFSExecutables = map[string]bool{
	"dd":      true,
	"shred":   true,
	"wipe":    true,
	"mkfs":    true,
	"fdisk":   true,
	"parted":  true,
	"mount":   true,
	"umount":  true,
}

var denyProcessKillExecutables = map[string]bool{
	"pkill":   true,
	"killall": true,
}

var denyCredentialExecutables = map[string]bool{
	"ssh-keygen": true,
	"keychain":   true,
}

// Cloud CLIs — default deny. A readonly subcommand prefix can rescue them.
var cloudCLIExecutables = map[string]bool{
	"aws":             true,
	"gcloud":          true,
	"kubectl":         true,
	"terraform":       true,
	"helm":            true,
	"ansible-playbook": true,
}

// Cloud CLI readonly subcommand prefixes — matched against the
// flag-stripped subcommand sequence as a space-joined string.
//
// Match is "exact prefix" — `kubectl get pods` matches prefix `get`, but
// `kubectl get pods -o yaml` is also fine because we only check the leading
// non-flag tokens.
var cloudCLIReadOnlyPrefixes = map[string][]string{
	"aws": {
		"s3 ls",
		"s3api list-",
		"sts get-caller-identity",
		"iam list-",
		"iam get-",
		"ec2 describe-",
		"logs describe-",
		"logs get-",
		"logs tail",
	},
	"gcloud": {
		"projects list",
		"projects describe",
		"compute instances list",
		"compute instances describe",
		"compute disks list",
		"compute disks describe",
		"run services list",
		"run services describe",
		"run revisions list",
		"sql instances list",
		"sql instances describe",
		"auth list",
		"config list",
		"config get-value",
		"info",
		"version",
	},
	"kubectl": {
		"get",
		"describe",
		"logs",
		"top",
		"explain",
		"version",
		"cluster-info",
		"api-resources",
		"api-versions",
		"config view",
		"config get-contexts",
		"config current-context",
		"diff",
	},
	"terraform": {
		"plan",
		"validate",
		"fmt",
		"show",
		"output",
		"version",
		"providers",
		"state list",
		"state show",
		"workspace list",
		"workspace show",
		"console",
	},
	"helm": {
		"list",
		"status",
		"history",
		"get",
		"template",
		"lint",
		"version",
		"search",
		"show",
		"env",
		"repo list",
	},
}

// System path prefixes — any absolute path operand starting with these is
// treated as out-of-workspace and requires approval.
var systemPathPrefixes = []string{
	"/etc/",
	"/usr/",
	"/var/",
	"/boot/",
	"/sys/",
	"/proc/",
	"/dev/",
	"/opt/",
	"/lib/",
	"/lib64/",
	"/bin/",
	"/sbin/",
}

// Sensitive credential / config paths. Substring match (handles both
// absolute paths and ~/.foo style references — model agents typically
// emit absolute paths after tilde expansion).
var credentialPathPatterns = []string{
	"/.ssh/id_",
	"/.ssh/identity",
	"/.ssh/authorized_keys",
	"/.aws/credentials",
	"/.aws/config",
	"/.kube/config",
	"/.config/gh/",
	"/.config/gh-cli/",
	"/.netrc",
	"/.docker/config.json",
	"/.gnupg/",
	"/.password-store/",
	"/.config/op/",
}

// Inline-script invocation: executable + flag combinations that take a
// script body as an argument (executes whatever the model wrote inline).
// We can't statically classify their bodies, so they always require approval.
var inlineScriptFlags = map[string]map[string]bool{
	"python":  {"-c": true},
	"python3": {"-c": true},
	"node":    {"-e": true, "--eval": true},
	"ruby":    {"-e": true},
	"perl":    {"-e": true},
	"deno":    {"eval": true},
}

// Shells whose appearance as a non-first pipeline segment OR with no script
// argument means "execute from stdin" — classic curl-pipe-to-shell vector.
var stdinInterpreterExecutables = map[string]bool{
	"bash":    true,
	"sh":      true,
	"zsh":     true,
	"fish":    true,
	"dash":    true,
	"ksh":     true,
	"python":  true,
	"python3": true,
	"node":    true,
	"ruby":    true,
	"perl":    true,
	"deno":    true,
}

// risk level mapping for back-compat with existing read-only profile gating
// and command-card UI.
func riskLevelForCategory(category string) string {
	switch category {
	case policyCategoryAllowRead,
		policyCategoryAllowCloudReadOnly,
		policyCategoryAllowGitFetch:
		return riskLevelSafeRead
	case policyCategoryAllowBuildTest,
		policyCategoryAllowWorkspaceFile,
		policyCategoryAllowLocalGit,
		policyCategoryAllowPackageInstall,
		policyCategoryAllowDefault:
		return riskLevelGuardedWrite
	default:
		// All other deny_* categories: model genuinely tried something
		// dangerous; mark destructive so the read-only profile gate still
		// allows the approval prompt to surface (vs being silently blocked).
		return riskLevelDestructive
	}
}

// denyTypeForCategory maps a deny category to the wire-format deny type
// stored on command.finished events for the command-card UI.
func denyTypeForCategory(category string) string {
	switch category {
	case policyCategoryDenyInlineScript:
		return commandDenyTypeInlineScript
	case policyCategoryDenyPrivilege:
		return commandDenyTypePrivilege
	case policyCategoryDenyPackageManager:
		return commandDenyTypeSystemPkgMgr
	case policyCategoryDenyDangerousFS:
		return commandDenyTypeDangerousFS
	case policyCategoryDenyDestructiveGit:
		return commandDenyTypeDestructiveGit
	case policyCategoryDenyPublish:
		return commandDenyTypePublish
	case policyCategoryDenyCloudCLI:
		return commandDenyTypeCloudCLI
	case policyCategoryDenyRemoteShell:
		return commandDenyTypeRemoteShell
	case policyCategoryDenySystemService:
		return commandDenyTypeSystemService
	case policyCategoryDenyContainerEscape:
		return commandDenyTypeContainerEscape
	case policyCategoryDenyNetworkPipe:
		return commandDenyTypeNetworkPipe
	case policyCategoryDenyCredential:
		return commandDenyTypeCredential
	case policyCategoryDenyWorkspaceEscape:
		return commandDenyTypeWorkspaceEscape
	case policyCategoryDenyProcessKill:
		return commandDenyTypeProcessKill
	case policyCategoryDenyGlobUnsafe:
		return commandDenyTypeGlobUnsafe
	case policyCategoryDenyUnparseable:
		return commandDenyTypeUnparseable
	default:
		return commandDenyTypePolicy
	}
}

// denyReasonForCategory returns the human-readable reason shown on the
// approval card and on rejected-command tool results.
func denyReasonForCategory(category string) string {
	switch category {
	case policyCategoryDenyInlineScript:
		return "内联脚本需要确认（python -c / node -e 等）"
	case policyCategoryDenyPrivilege:
		return "提权命令需要用户确认（sudo / su / doas / pkexec）"
	case policyCategoryDenyPackageManager:
		return "系统包管理器写操作需要用户确认"
	case policyCategoryDenyDangerousFS:
		return "破坏性文件系统操作需要用户确认"
	case policyCategoryDenyDestructiveGit:
		return "破坏性 git 操作需要用户确认（reset --hard / push / amend 等）"
	case policyCategoryDenyPublish:
		return "外部发布 / 推送需要用户确认"
	case policyCategoryDenyCloudCLI:
		return "云 CLI 写操作需要用户确认（白名单外的子命令）"
	case policyCategoryDenyRemoteShell:
		return "远程 shell / 文件传输需要用户确认"
	case policyCategoryDenySystemService:
		return "系统服务管理需要用户确认"
	case policyCategoryDenyContainerEscape:
		return "容器特权 / 主机挂载需要用户确认"
	case policyCategoryDenyNetworkPipe:
		return "网络脚本管道执行需要用户确认（curl | bash 模式）"
	case policyCategoryDenyCredential:
		return "访问凭据 / 密钥路径需要用户确认"
	case policyCategoryDenyWorkspaceEscape:
		return "目标路径在 workspace 之外，需要用户确认"
	case policyCategoryDenyProcessKill:
		return "进程管理命令需要用户确认（pkill / killall / kill -9）"
	case policyCategoryDenyGlobUnsafe:
		return "带通配符的高危命令需要用户确认"
	case policyCategoryDenyUnparseable:
		return "无法解析的命令片段，需要用户确认"
	default:
		return "命令需要用户确认"
	}
}
