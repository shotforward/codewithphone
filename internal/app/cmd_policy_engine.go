package app

import (
	"path/filepath"
	"strings"
)

// commandPolicyDecision is the result of evaluating a command against the
// allow / deny ruleset. Allow=true means auto-execute without prompting;
// Allow=false means surface a user approval card.
type commandPolicyDecision struct {
	Allow      bool
	Category   string
	DenyType   string
	DenyReason string
}

func decisionAllow(category string) commandPolicyDecision {
	return commandPolicyDecision{Allow: true, Category: category}
}

func decisionDeny(category string) commandPolicyDecision {
	return commandPolicyDecision{
		Allow:      false,
		Category:   category,
		DenyType:   denyTypeForCategory(category),
		DenyReason: denyReasonForCategory(category),
	}
}

// evaluatePolicy is the canonical entry point: feed it the raw command text
// the model produced (possibly a shell pipeline or wrapped in `bash -c`),
// get back the auto-run vs. ask-user decision.
func evaluatePolicy(rawCommand string) commandPolicyDecision {
	script := strings.TrimSpace(rawCommand)
	if script == "" {
		return decisionAllow(policyCategoryAllowDefault)
	}
	// Strip one outer shell wrapper so the body is treated uniformly. The
	// body itself may still contain pipes, &&, ||, ;.
	script = unwrapShellWrapperCommandText(script)
	return evaluatePolicyScript(script)
}

// evaluatePolicyScript walks a script's pipeline / sequence segments and
// returns the strongest decision. Any deny short-circuits.
func evaluatePolicyScript(script string) commandPolicyDecision {
	script = strings.TrimSpace(script)
	if script == "" {
		return decisionAllow(policyCategoryAllowDefault)
	}

	// Process substitution mixed with network fetch is the classic
	// `bash <(curl ...)` exploit. We catch it before segment-splitting
	// because process-sub fragments don't survive the naive splitter.
	if strings.Contains(script, "<(") && containsNetworkFetchToken(script) {
		return decisionDeny(policyCategoryDenyNetworkPipe)
	}

	segments := splitShellSegments(script)
	if len(segments) == 0 {
		return decisionAllow(policyCategoryAllowDefault)
	}

	first := commandPolicyDecision{}
	haveFirst := false
	for idx, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		executable, args, ok := parsePolicySegment(seg)
		if !ok {
			return decisionDeny(policyCategoryDenyUnparseable)
		}

		// Stdin-shell detection: a shell/interpreter appearing as a
		// downstream pipeline segment with no script arg = exec from stdin.
		if idx > 0 && isStdinShellSegment(executable, args) {
			return decisionDeny(policyCategoryDenyNetworkPipe)
		}

		// Peel wrappers (env / nohup / time / nice / timeout / xargs)
		executable, args = peelCommandWrappers(executable, args)

		// `bash -c BODY` → recurse on BODY
		if isShellWrapper(executable) {
			body, hasBody := extractShellScript(executable, args)
			if !hasBody || strings.TrimSpace(body) == "" {
				return decisionDeny(policyCategoryDenyNetworkPipe)
			}
			inner := evaluatePolicyScript(body)
			if !inner.Allow {
				return inner
			}
			if !haveFirst {
				first = inner
				haveFirst = true
			}
			continue
		}

		d := evaluatePolicySingle(executable, args)
		if !d.Allow {
			return d
		}
		if !haveFirst {
			first = d
			haveFirst = true
		}
	}
	if !haveFirst {
		return decisionAllow(policyCategoryAllowDefault)
	}
	return first
}

func parsePolicySegment(segment string) (string, []string, bool) {
	fields := splitShellWords(segment)
	if len(fields) == 0 {
		return "", nil, false
	}
	return canonicalExecutableName(fields[0]), fields[1:], true
}

// evaluatePolicySingle classifies a single command. Callers may pass a
// wrapper (env / nohup / xargs / etc.) — we peel them here so test-style
// direct calls behave the same as pipeline-segment evaluation.
func evaluatePolicySingle(executable string, args []string) commandPolicyDecision {
	executable, args = peelCommandWrappers(canonicalExecutableName(executable), args)
	if executable == "" {
		return decisionDeny(policyCategoryDenyUnparseable)
	}

	// `bash -c BODY` arriving through this path → evaluate the body.
	if isShellWrapper(executable) {
		body, hasBody := extractShellScript(executable, args)
		if !hasBody || strings.TrimSpace(body) == "" {
			return decisionDeny(policyCategoryDenyNetworkPipe)
		}
		return evaluatePolicyScript(body)
	}

	// Inline scripts (python -c '...', node -e '...', etc.) can't be
	// statically classified — always ask.
	if isInlineScriptInvocation(executable, args) {
		return decisionDeny(policyCategoryDenyInlineScript)
	}

	// Path-operand checks: any operand pointing to system paths or
	// credential paths triggers an approval prompt.
	if d, hit := checkOperandPathPolicy(args); hit {
		return d
	}

	// Always-deny executable buckets
	if denyPrivilegeExecutables[executable] {
		return decisionDeny(policyCategoryDenyPrivilege)
	}
	if denySystemPackageManagers[executable] {
		return decisionDeny(policyCategoryDenyPackageManager)
	}
	if denyDestructiveFSExecutables[executable] {
		return decisionDeny(policyCategoryDenyDangerousFS)
	}
	if denySystemServiceExecutables[executable] {
		return decisionDeny(policyCategoryDenySystemService)
	}
	if denyProcessKillExecutables[executable] {
		return decisionDeny(policyCategoryDenyProcessKill)
	}
	if denyCredentialExecutables[executable] {
		return decisionDeny(policyCategoryDenyCredential)
	}
	if denyRemoteShellExecutables[executable] {
		return decisionDeny(policyCategoryDenyRemoteShell)
	}

	// Per-executable evaluators
	switch executable {
	case "git":
		return evaluatePolicyGit(args)
	case "docker", "podman":
		return evaluatePolicyDocker(args)
	case "rm":
		return evaluatePolicyRm(args)
	case "find":
		return evaluatePolicyFind(args)
	case "chmod", "chown":
		return evaluatePolicyChmodChown(args)
	case "kill":
		return evaluatePolicyKill(args)
	case "rsync":
		return evaluatePolicyRsync(args)
	case "npm", "pnpm", "yarn", "bun":
		return evaluatePolicyNpm(args)
	case "pip", "pip3":
		return evaluatePolicyPip(args)
	case "cargo":
		return evaluatePolicyCargo(args)
	case "go":
		return decisionAllow(policyCategoryAllowBuildTest)
	case "make":
		return decisionAllow(policyCategoryAllowBuildTest)
	case "gh":
		return evaluatePolicyGh(args)
	case "brew":
		return evaluatePolicyBrew(args)
	case "crontab":
		return evaluatePolicyCrontab(args)
	case "tee":
		return decisionAllow(policyCategoryAllowWorkspaceFile)
	case "cat":
		return evaluatePolicyCat(args)
	case "sed":
		return evaluatePolicySed(args)
	}

	// Cloud CLIs default to deny; only whitelisted readonly subcommands pass.
	if cloudCLIExecutables[executable] {
		return evaluatePolicyCloudCLI(executable, args)
	}

	// Writable redirection on a generic command → workspace-file mutation.
	// This must come AFTER per-executable dispatch so we don't accidentally
	// override a stricter classification (e.g. rm with a redirect).
	if hasWritableRedirection(args) {
		return decisionAllow(policyCategoryAllowWorkspaceFile)
	}

	// Allow buckets
	if allowReadExecutables[executable] {
		return decisionAllow(policyCategoryAllowRead)
	}
	if allowBuildTestExecutables[executable] {
		return decisionAllow(policyCategoryAllowBuildTest)
	}
	if allowWorkspaceFileExecutables[executable] {
		return decisionAllow(policyCategoryAllowWorkspaceFile)
	}

	// Default-allow (mode A) for unknown commands.
	return decisionAllow(policyCategoryAllowDefault)
}

// ---- per-executable evaluators ----

func evaluatePolicyGit(args []string) commandPolicyDecision {
	sub, rest := extractGitSubcommand(args)
	if sub == "" {
		return decisionAllow(policyCategoryAllowRead)
	}
	switch sub {
	case "status", "diff", "log", "show", "blame", "annotate",
		"ls-files", "ls-tree", "ls-remote", "rev-parse", "rev-list", "reflog",
		"describe", "name-rev", "shortlog", "var", "grep":
		return decisionAllow(policyCategoryAllowRead)
	case "tag":
		for _, a := range rest {
			if a == "-d" || a == "--delete" {
				return decisionAllow(policyCategoryAllowLocalGit)
			}
		}
		// `git tag` with no args lists; with a name creates a local tag.
		hasName := false
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				hasName = true
				break
			}
		}
		if !hasName {
			return decisionAllow(policyCategoryAllowRead)
		}
		return decisionAllow(policyCategoryAllowLocalGit)
	case "config":
		for _, a := range rest {
			if a == "--unset" || a == "--unset-all" || a == "--add" ||
				a == "--rename-section" || a == "--remove-section" {
				return decisionAllow(policyCategoryAllowLocalGit)
			}
		}
		// Setting a value via `git config key value` is local mutation,
		// reading via `--get` / no extra flags is read.
		nonFlag := 0
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				nonFlag++
			}
		}
		if nonFlag >= 2 {
			return decisionAllow(policyCategoryAllowLocalGit)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "branch":
		for _, a := range rest {
			if a == "-D" || a == "-d" || a == "--delete" ||
				a == "-m" || a == "--move" || a == "-c" || a == "--copy" {
				return decisionAllow(policyCategoryAllowLocalGit)
			}
		}
		hasName := false
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				continue
			}
			hasName = true
		}
		if !hasName {
			return decisionAllow(policyCategoryAllowRead)
		}
		return decisionAllow(policyCategoryAllowLocalGit)
	case "remote", "stash":
		return decisionAllow(policyCategoryAllowLocalGit)
	case "fetch", "pull":
		return decisionAllow(policyCategoryAllowGitFetch)
	case "add", "restore", "checkout", "switch", "mv", "rm",
		"merge", "cherry-pick", "revert", "init", "clone",
		"worktree", "submodule", "gc", "apply", "format-patch":
		return decisionAllow(policyCategoryAllowLocalGit)
	case "commit":
		for _, a := range rest {
			if a == "--amend" {
				return decisionDeny(policyCategoryDenyDestructiveGit)
			}
		}
		return decisionAllow(policyCategoryAllowLocalGit)
	case "reset":
		for _, a := range rest {
			if a == "--hard" {
				return decisionDeny(policyCategoryDenyDestructiveGit)
			}
		}
		return decisionAllow(policyCategoryAllowLocalGit)
	case "rebase":
		for _, a := range rest {
			if a == "-i" || a == "--interactive" {
				return decisionDeny(policyCategoryDenyDestructiveGit)
			}
		}
		return decisionAllow(policyCategoryAllowLocalGit)
	case "clean":
		return decisionDeny(policyCategoryDenyDestructiveGit)
	case "push":
		return decisionDeny(policyCategoryDenyPublish)
	case "filter-branch", "filter-repo":
		return decisionDeny(policyCategoryDenyDestructiveGit)
	}
	return decisionAllow(policyCategoryAllowDefault)
}

func extractGitSubcommand(args []string) (string, []string) {
	idx := 0
	for idx < len(args) {
		arg := args[idx]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		// `git -C <path> ...`, `git -c key=value ...`, `git --git-dir ...`
		if arg == "-C" || arg == "-c" || arg == "--git-dir" ||
			arg == "--work-tree" || arg == "--namespace" {
			if idx+1 < len(args) {
				idx += 2
				continue
			}
		}
		idx++
	}
	if idx >= len(args) {
		return "", nil
	}
	return args[idx], args[idx+1:]
}

func extractSubcommand(args []string) (string, []string) {
	idx := 0
	for idx < len(args) && strings.HasPrefix(args[idx], "-") {
		idx++
	}
	if idx >= len(args) {
		return "", nil
	}
	return args[idx], args[idx+1:]
}

func evaluatePolicyDocker(args []string) commandPolicyDecision {
	sub, rest := extractSubcommand(args)
	switch sub {
	case "":
		return decisionAllow(policyCategoryAllowDefault)
	case "push", "login", "logout":
		return decisionDeny(policyCategoryDenyPublish)
	case "run", "exec", "create":
		for idx := 0; idx < len(rest); idx++ {
			arg := rest[idx]
			switch arg {
			case "--privileged", "--cap-add", "--pid=host", "--net=host",
				"--network=host", "--ipc=host", "--userns=host":
				return decisionDeny(policyCategoryDenyContainerEscape)
			}
			if strings.HasPrefix(arg, "--cap-add=") ||
				strings.HasPrefix(arg, "--pid=") && arg == "--pid=host" ||
				strings.HasPrefix(arg, "--network=") && arg == "--network=host" {
				return decisionDeny(policyCategoryDenyContainerEscape)
			}
			if arg == "--security-opt" && idx+1 < len(rest) &&
				strings.Contains(rest[idx+1], "seccomp=unconfined") {
				return decisionDeny(policyCategoryDenyContainerEscape)
			}
			if strings.HasPrefix(arg, "--security-opt=") &&
				strings.Contains(arg, "seccomp=unconfined") {
				return decisionDeny(policyCategoryDenyContainerEscape)
			}
			if arg == "-v" || arg == "--volume" || arg == "--mount" {
				if idx+1 < len(rest) && dockerMountEscapesHost(rest[idx+1]) {
					return decisionDeny(policyCategoryDenyContainerEscape)
				}
			} else if strings.HasPrefix(arg, "-v=") ||
				strings.HasPrefix(arg, "--volume=") ||
				strings.HasPrefix(arg, "--mount=") {
				eq := strings.Index(arg, "=")
				if eq >= 0 && dockerMountEscapesHost(arg[eq+1:]) {
					return decisionDeny(policyCategoryDenyContainerEscape)
				}
			}
		}
		return decisionAllow(policyCategoryAllowDefault)
	case "build", "compose", "ps", "logs", "images", "history",
		"version", "info", "inspect", "stats", "events", "system", "context":
		return decisionAllow(policyCategoryAllowBuildTest)
	case "pull", "rm", "rmi", "stop", "start", "restart", "kill",
		"network", "volume", "container", "image":
		return decisionAllow(policyCategoryAllowDefault)
	}
	return decisionAllow(policyCategoryAllowDefault)
}

func dockerMountEscapesHost(spec string) bool {
	src := spec
	if strings.Contains(spec, ",source=") || strings.Contains(spec, ",src=") {
		for _, part := range strings.Split(spec, ",") {
			switch {
			case strings.HasPrefix(part, "source="):
				src = strings.TrimPrefix(part, "source=")
			case strings.HasPrefix(part, "src="):
				src = strings.TrimPrefix(part, "src=")
			}
		}
	} else {
		idx := strings.Index(spec, ":")
		if idx >= 0 {
			src = spec[:idx]
		}
	}
	src = strings.TrimSpace(src)
	if src == "/" {
		return true
	}
	if !filepath.IsAbs(src) {
		return false
	}
	check := src + "/"
	for _, p := range systemPathPrefixes {
		if strings.HasPrefix(check, p) {
			return true
		}
	}
	for _, p := range credentialPathPatterns {
		if strings.Contains(check, p) {
			return true
		}
	}
	return false
}

func evaluatePolicyRm(args []string) commandPolicyDecision {
	for _, arg := range args {
		if arg == "--recursive" {
			return decisionDeny(policyCategoryDenyDangerousFS)
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 1 {
			for _, c := range arg[1:] {
				if c == 'r' || c == 'R' {
					return decisionDeny(policyCategoryDenyDangerousFS)
				}
			}
		}
		if !strings.HasPrefix(arg, "-") && strings.ContainsAny(arg, "*?[") {
			return decisionDeny(policyCategoryDenyGlobUnsafe)
		}
	}
	return decisionAllow(policyCategoryAllowWorkspaceFile)
}

func evaluatePolicyFind(args []string) commandPolicyDecision {
	for idx := 0; idx < len(args); idx++ {
		arg := args[idx]
		if arg == "-delete" {
			return decisionDeny(policyCategoryDenyDangerousFS)
		}
		if arg == "-exec" || arg == "-execdir" || arg == "-ok" || arg == "-okdir" {
			body := []string{}
			i := idx + 1
			for i < len(args) && args[i] != ";" && args[i] != "+" && args[i] != "\\;" {
				body = append(body, args[i])
				i++
			}
			if len(body) == 0 {
				return decisionDeny(policyCategoryDenyUnparseable)
			}
			inner := evaluatePolicySingle(body[0], body[1:])
			if !inner.Allow {
				return inner
			}
			idx = i
		}
	}
	return decisionAllow(policyCategoryAllowRead)
}

func evaluatePolicyChmodChown(args []string) commandPolicyDecision {
	for _, arg := range args {
		if arg == "-R" || arg == "--recursive" {
			return decisionDeny(policyCategoryDenyDangerousFS)
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 1 {
			for _, c := range arg[1:] {
				if c == 'R' {
					return decisionDeny(policyCategoryDenyDangerousFS)
				}
			}
		}
	}
	return decisionAllow(policyCategoryAllowWorkspaceFile)
}

func evaluatePolicyKill(args []string) commandPolicyDecision {
	if len(args) == 0 {
		return decisionAllow(policyCategoryAllowDefault)
	}
	if args[0] == "-l" || args[0] == "-L" || args[0] == "--list" {
		return decisionAllow(policyCategoryAllowRead)
	}
	return decisionDeny(policyCategoryDenyProcessKill)
}

func evaluatePolicyRsync(args []string) commandPolicyDecision {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.HasPrefix(arg, "rsync://") {
			return decisionDeny(policyCategoryDenyRemoteShell)
		}
		if hasRemoteHostSeparator(arg) {
			return decisionDeny(policyCategoryDenyRemoteShell)
		}
	}
	return decisionAllow(policyCategoryAllowWorkspaceFile)
}

func hasRemoteHostSeparator(arg string) bool {
	colon := strings.Index(arg, ":")
	if colon <= 0 {
		return false
	}
	prefix := arg[:colon]
	// Path with colon (rare on unix) — has a slash before the colon
	if strings.ContainsAny(prefix, "/.") {
		return false
	}
	return true
}

func evaluatePolicyNpm(args []string) commandPolicyDecision {
	sub, _ := extractSubcommand(args)
	switch sub {
	case "publish":
		return decisionDeny(policyCategoryDenyPublish)
	case "install", "i", "add", "ci", "uninstall", "remove", "rm",
		"audit", "fund", "outdated", "ls", "list", "view", "search",
		"run", "test", "build", "lint", "format", "exec", "start",
		"version", "init", "create", "link", "unlink", "config",
		"why", "info", "show", "dlx":
		return decisionAllow(policyCategoryAllowPackageInstall)
	}
	return decisionAllow(policyCategoryAllowDefault)
}

func evaluatePolicyPip(args []string) commandPolicyDecision {
	sub, _ := extractSubcommand(args)
	switch sub {
	case "upload":
		return decisionDeny(policyCategoryDenyPublish)
	case "install", "uninstall", "list", "show", "freeze", "check",
		"config", "download", "wheel":
		return decisionAllow(policyCategoryAllowPackageInstall)
	}
	return decisionAllow(policyCategoryAllowDefault)
}

func evaluatePolicyCargo(args []string) commandPolicyDecision {
	sub, _ := extractSubcommand(args)
	switch sub {
	case "publish", "yank":
		return decisionDeny(policyCategoryDenyPublish)
	case "build", "test", "check", "fmt", "clippy", "run", "doc",
		"bench", "install", "uninstall", "update", "version", "tree",
		"search", "info", "init", "new", "add", "remove", "fix":
		return decisionAllow(policyCategoryAllowBuildTest)
	}
	return decisionAllow(policyCategoryAllowDefault)
}

func evaluatePolicyGh(args []string) commandPolicyDecision {
	sub, rest := extractSubcommand(args)
	switch sub {
	case "auth":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "login", "logout", "setup-git", "refresh":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "pr":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "merge", "close", "create", "edit", "reopen", "ready":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "release":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "create", "delete", "edit", "upload":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "repo":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "create", "delete", "fork", "rename", "edit", "archive", "unarchive":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "workflow":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "run", "enable", "disable":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "secret", "variable":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "set", "delete", "remove":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "issue":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "close", "create", "delete", "edit", "reopen", "lock", "unlock", "transfer":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	case "gist":
		sub2, _ := extractSubcommand(rest)
		switch sub2 {
		case "create", "delete", "edit":
			return decisionDeny(policyCategoryDenyPublish)
		}
		return decisionAllow(policyCategoryAllowRead)
	}
	return decisionAllow(policyCategoryAllowDefault)
}

func evaluatePolicyBrew(args []string) commandPolicyDecision {
	sub, _ := extractSubcommand(args)
	switch sub {
	case "install", "upgrade", "uninstall", "reinstall", "remove", "rm",
		"tap", "untap", "cask", "pin", "unpin":
		return decisionDeny(policyCategoryDenyPackageManager)
	}
	return decisionAllow(policyCategoryAllowRead)
}

func evaluatePolicyCrontab(args []string) commandPolicyDecision {
	for _, arg := range args {
		if arg == "-r" || arg == "-e" || arg == "--remove" {
			return decisionDeny(policyCategoryDenySystemService)
		}
	}
	return decisionAllow(policyCategoryAllowRead)
}

func evaluatePolicyCat(args []string) commandPolicyDecision {
	if hasWritableRedirection(args) {
		return decisionAllow(policyCategoryAllowWorkspaceFile)
	}
	for _, arg := range args {
		if arg == "" {
			continue
		}
		if isRedirectionOperatorToken(arg) || isRedirectionLikeToken(arg) {
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		return decisionAllow(policyCategoryAllowRead)
	}
	// cat with no readable input — model probably wanted stdin pipe but
	// outside a pipeline it'll hang; preserve historical "ask" semantics.
	return decisionAllow(policyCategoryAllowDefault)
}

func evaluatePolicySed(args []string) commandPolicyDecision {
	for _, arg := range args {
		if arg == "-i" || strings.HasPrefix(arg, "-i") && !strings.HasPrefix(arg, "--") {
			return decisionAllow(policyCategoryAllowWorkspaceFile)
		}
		if arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") {
			return decisionAllow(policyCategoryAllowWorkspaceFile)
		}
	}
	return decisionAllow(policyCategoryAllowRead)
}

// evaluatePolicyCloudCLI: default deny; whitelist of read subcommand prefixes.
func evaluatePolicyCloudCLI(executable string, args []string) commandPolicyDecision {
	prefixes := cloudCLIReadOnlyPrefixes[executable]
	if len(prefixes) == 0 {
		return decisionDeny(policyCategoryDenyCloudCLI)
	}

	nonFlag := []string{}
	skipNext := false
	for idx, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		// Skip common context/region/profile flags that take a value
		switch arg {
		case "--context", "--namespace", "-n", "--profile", "--region",
			"--project", "--cluster", "--kubeconfig", "--output", "-o":
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		nonFlag = append(nonFlag, arg)
		_ = idx
	}
	if len(nonFlag) == 0 {
		return decisionDeny(policyCategoryDenyCloudCLI)
	}

	joined := strings.Join(nonFlag, " ")
	for _, p := range prefixes {
		if joined == p ||
			strings.HasPrefix(joined, p+" ") ||
			strings.HasPrefix(joined, p) && strings.HasSuffix(p, "-") {
			return decisionAllow(policyCategoryAllowCloudReadOnly)
		}
	}
	return decisionDeny(policyCategoryDenyCloudCLI)
}

// ---- helpers ----

func isInlineScriptInvocation(executable string, args []string) bool {
	flags, ok := inlineScriptFlags[canonicalExecutableName(executable)]
	if !ok {
		return false
	}
	for _, arg := range args {
		if flags[arg] {
			return true
		}
	}
	return false
}

// isStdinShellSegment returns true when `executable args` is a shell or
// scripting interpreter that will execute whatever arrives on stdin.
func isStdinShellSegment(executable string, args []string) bool {
	if !stdinInterpreterExecutables[canonicalExecutableName(executable)] {
		return false
	}
	for _, arg := range args {
		if arg == "-c" || arg == "-lc" || arg == "-cl" || arg == "-e" || arg == "--eval" || arg == "eval" {
			// has explicit script body — not a stdin executor
			return false
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// First non-flag arg → treated as script file (not stdin)
		return false
	}
	return true
}

func peelCommandWrappers(executable string, args []string) (string, []string) {
	for {
		switch executable {
		case "env":
			idx := 0
			for idx < len(args) {
				arg := args[idx]
				if arg == "-i" || arg == "--ignore-environment" {
					idx++
					continue
				}
				if arg == "-u" && idx+1 < len(args) {
					idx += 2
					continue
				}
				if strings.HasPrefix(arg, "-") && arg != "-" {
					idx++
					continue
				}
				if strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-") {
					idx++
					continue
				}
				break
			}
			if idx >= len(args) {
				return executable, args
			}
			executable = canonicalExecutableName(args[idx])
			args = args[idx+1:]
		case "nohup":
			if len(args) == 0 {
				return executable, args
			}
			executable = canonicalExecutableName(args[0])
			args = args[1:]
		case "time":
			if len(args) == 0 {
				return executable, args
			}
			executable = canonicalExecutableName(args[0])
			args = args[1:]
		case "nice":
			idx := 0
			if idx < len(args) && args[idx] == "-n" {
				idx += 2
			} else if idx < len(args) && strings.HasPrefix(args[idx], "-n") {
				idx++
			}
			if idx >= len(args) {
				return executable, args
			}
			executable = canonicalExecutableName(args[idx])
			args = args[idx+1:]
		case "timeout":
			idx := 0
			for idx < len(args) && strings.HasPrefix(args[idx], "-") {
				arg := args[idx]
				if arg == "-s" || arg == "--signal" || arg == "-k" || arg == "--kill-after" {
					if idx+1 < len(args) {
						idx += 2
						continue
					}
				}
				idx++
			}
			// Skip the duration token
			if idx < len(args) {
				idx++
			}
			if idx >= len(args) {
				return executable, args
			}
			executable = canonicalExecutableName(args[idx])
			args = args[idx+1:]
		case "xargs":
			idx := 0
			for idx < len(args) && strings.HasPrefix(args[idx], "-") {
				arg := args[idx]
				if arg == "-I" || arg == "-n" || arg == "-P" || arg == "-L" ||
					arg == "-d" || arg == "-E" || arg == "-a" || arg == "-s" {
					if idx+1 < len(args) {
						idx += 2
						continue
					}
				}
				idx++
			}
			if idx >= len(args) {
				return executable, args
			}
			executable = canonicalExecutableName(args[idx])
			args = args[idx+1:]
		default:
			return executable, args
		}
	}
}

// checkOperandPathPolicy walks args looking for absolute paths that point
// outside the workspace (system dirs) or at sensitive credentials. Returns
// the matching deny decision when found.
func checkOperandPathPolicy(args []string) (commandPolicyDecision, bool) {
	for idx, arg := range args {
		if arg == "" {
			continue
		}
		// Pure flags carry no path operand.
		if strings.HasPrefix(arg, "--") {
			eq := strings.Index(arg, "=")
			if eq < 0 {
				continue
			}
			arg = arg[eq+1:]
		} else if strings.HasPrefix(arg, "-") && arg != "-" {
			// short flag bundle like -rf, -la — no path operand
			continue
		}

		candidate := arg
		// `>filename`, `2>filename`, etc. — strip operator prefix
		switch {
		case strings.HasPrefix(candidate, "2>>"):
			candidate = candidate[3:]
		case strings.HasPrefix(candidate, "1>>"):
			candidate = candidate[3:]
		case strings.HasPrefix(candidate, "2>"):
			candidate = candidate[2:]
		case strings.HasPrefix(candidate, "1>"):
			candidate = candidate[2:]
		case strings.HasPrefix(candidate, ">>"):
			candidate = candidate[2:]
		case strings.HasPrefix(candidate, ">"):
			candidate = candidate[1:]
		case strings.HasPrefix(candidate, "<"):
			candidate = candidate[1:]
		}
		// `> filename` and `2> filename` — operator + next token form
		if isRedirectionOperatorToken(arg) {
			if idx+1 < len(args) {
				candidate = args[idx+1]
			} else {
				continue
			}
		}

		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "~/") {
			// Treat ~/X as if rooted at /home/<user>/X — the OS will expand
			// tilde before exec, so for policy purposes treat it as home.
			if d, ok := credentialPathMatch(candidate); ok {
				return d, true
			}
			continue
		}
		if !filepath.IsAbs(candidate) {
			continue
		}
		if isStandardDevDescriptor(candidate) {
			continue
		}
		check := candidate
		if !strings.HasSuffix(check, "/") {
			check += "/"
		}
		for _, p := range systemPathPrefixes {
			if strings.HasPrefix(check, p) {
				return decisionDeny(policyCategoryDenyWorkspaceEscape), true
			}
		}
		if d, ok := credentialPathMatch(candidate); ok {
			return d, true
		}
	}
	return commandPolicyDecision{}, false
}

// isStandardDevDescriptor exempts the de-facto stdio handles from system
// path checks. Without this, anything redirecting to /dev/null would trip
// the workspace-escape rule.
func isStandardDevDescriptor(path string) bool {
	switch path {
	case "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/zero", "/dev/tty":
		return true
	}
	if strings.HasPrefix(path, "/dev/fd/") {
		return true
	}
	return false
}

func credentialPathMatch(path string) (commandPolicyDecision, bool) {
	for _, pat := range credentialPathPatterns {
		if strings.Contains(path, pat) {
			return decisionDeny(policyCategoryDenyCredential), true
		}
	}
	return commandPolicyDecision{}, false
}

func containsNetworkFetchToken(script string) bool {
	for _, field := range splitShellWords(script) {
		switch canonicalExecutableName(field) {
		case "curl", "wget", "http", "httpie", "httpx", "fetch", "aria2c":
			return true
		}
	}
	return false
}
