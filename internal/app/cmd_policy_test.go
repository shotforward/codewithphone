package app

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRunCommand(t *testing.T) {
	cmd, err := normalizeRunCommand(runCommandRequest{
		Executable: "git",
		Args:       []string{"status", "--short"},
		CWD:        ".",
		Reason:     "inspect working tree",
	})
	if err != nil {
		t.Fatalf("normalize run command: %v", err)
	}
	if cmd.RiskLevel != riskLevelSafeRead {
		t.Fatalf("expected safe_read, got %s", cmd.RiskLevel)
	}
	if cmd.Fingerprint == "" {
		t.Fatal("expected fingerprint to be populated")
	}
	if !shouldAutoApprove(cmd) {
		t.Fatal("expected safe_read command to auto approve")
	}
}

func TestNormalizeRunCommandRejectsShellWrappers(t *testing.T) {
	_, err := normalizeRunCommand(runCommandRequest{
		Executable: "bash",
		Args:       []string{"-lc", "rm -rf ."},
		CWD:        ".",
		Reason:     "dangerous",
	})
	if err == nil {
		t.Fatal("expected shell wrapper to be rejected")
	}
}

func TestClassifyCommandRisk(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		args       []string
		want       string
	}{
		{name: "du is safe read", executable: "du", args: []string{"-sh", "."}, want: riskLevelSafeRead},
		{name: "npm install is guarded", executable: "npm", args: []string{"install"}, want: riskLevelGuardedWrite},
		{name: "rm is destructive", executable: "rm", args: []string{"-rf", "."}, want: riskLevelDestructive},
		{name: "git reset hard is destructive", executable: "git", args: []string{"reset", "--hard"}, want: riskLevelDestructive},
		{name: "git branch show current is safe", executable: "git", args: []string{"branch", "--show-current"}, want: riskLevelSafeRead},
		{name: "git branch create is guarded", executable: "git", args: []string{"branch", "feature/demo"}, want: riskLevelGuardedWrite},
		{name: "sed -n is safe", executable: "sed", args: []string{"-n", "1,20p", "README.md"}, want: riskLevelSafeRead},
		{name: "sed -i is guarded", executable: "sed", args: []string{"-i", "s/a/b/", "README.md"}, want: riskLevelGuardedWrite},
		{name: "cat read file is safe", executable: "cat", args: []string{"README.md"}, want: riskLevelSafeRead},
		{name: "cat no input is guarded", executable: "cat", args: []string{}, want: riskLevelGuardedWrite},
		{name: "cat write redirect is guarded", executable: "cat", args: []string{">", "README.md"}, want: riskLevelGuardedWrite},
		{name: "stderr redirect to file is guarded", executable: "ls", args: []string{"2>errors.log"}, want: riskLevelGuardedWrite},
		{name: "find delete is destructive", executable: "find", args: []string{".", "-delete"}, want: riskLevelDestructive},
		{name: "xargs sed print is safe", executable: "xargs", args: []string{"-r", "sed", "-n", "1,260p"}, want: riskLevelSafeRead},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCommandRisk(tc.executable, tc.args); got != tc.want {
				t.Fatalf("classifyCommandRisk() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAllowsSessionApprovalForRisk(t *testing.T) {
	if allowsSessionApprovalForRisk(riskLevelDestructive) {
		t.Fatal("destructive commands must not support session scope approval")
	}
	if !allowsSessionApprovalForRisk(riskLevelGuardedWrite) {
		t.Fatal("guarded commands should support session scope approval")
	}
}

func TestAllowsCommandForProfile(t *testing.T) {
	readOnly := turnExecutionProfile{ReadOnly: true}
	writeMode := turnExecutionProfile{}

	safeCmd := normalizedCommand{RiskLevel: riskLevelSafeRead}
	if !allowsCommandForProfile(readOnly, safeCmd) {
		t.Fatal("expected safe read command to be allowed in read-only profile")
	}

	guardedCmd := normalizedCommand{RiskLevel: riskLevelGuardedWrite}
	if allowsCommandForProfile(readOnly, guardedCmd) {
		t.Fatal("expected guarded command to be blocked in read-only profile")
	}
	destructiveCmd := normalizedCommand{RiskLevel: riskLevelDestructive}
	if !allowsCommandForProfile(readOnly, destructiveCmd) {
		t.Fatal("expected destructive command to be approval-eligible in read-only profile")
	}
	if !allowsCommandForProfile(writeMode, guardedCmd) {
		t.Fatal("expected guarded command to be allowed in writable profile")
	}
}

func TestNormalizeCommandText(t *testing.T) {
	cmd := normalizeCommandText("du -sh .", ".", "measure workspace size")
	if cmd.Executable != "du" {
		t.Fatalf("expected executable du, got %s", cmd.Executable)
	}
	if cmd.RiskLevel != riskLevelSafeRead {
		t.Fatalf("expected safe_read risk, got %s", cmd.RiskLevel)
	}
	if cmd.Fingerprint == "" {
		t.Fatal("expected fingerprint to be generated")
	}

	complex := normalizeCommandText("cat foo.txt | wc -l", ".", "count lines")
	if complex.RiskLevel != riskLevelSafeRead {
		t.Fatalf("expected read-only pipeline to stay safe_read, got %s", complex.RiskLevel)
	}
}

func TestNormalizeCommandTextShellWrapperReadOnly(t *testing.T) {
	cmd := normalizeCommandText(`/bin/bash -lc "find . -maxdepth 2 -type f 2>/dev/null | sort"`, ".", "inspect project files")
	if cmd.Executable != "find" {
		t.Fatalf("expected unwrapped executable find, got %s", cmd.Executable)
	}
	if cmd.RiskLevel != riskLevelSafeRead {
		t.Fatalf("expected shell-wrapped read-only command to stay safe_read, got %s", cmd.RiskLevel)
	}
	if !shouldAutoApprove(cmd) {
		t.Fatal("expected shell-wrapped read-only command to auto approve")
	}
}

func TestNormalizeCommandTextShellWrapperReadOnlyWithXargs(t *testing.T) {
	cmd := normalizeCommandText(`/bin/bash -lc "find server/internal -type f | sort | xargs -r sed -n '1,260p'"`, ".", "inspect source files")
	if cmd.RiskLevel != riskLevelSafeRead {
		t.Fatalf("expected xargs read-only pipeline to be safe_read, got %s", cmd.RiskLevel)
	}
}

func TestNormalizeCommandTextShellWrapperWrite(t *testing.T) {
	cmd := normalizeCommandText(`/bin/bash -lc "echo hi > README.tmp"`, ".", "write file")
	if cmd.RiskLevel != riskLevelGuardedWrite {
		t.Fatalf("expected redirected write command to be guarded_write, got %s", cmd.RiskLevel)
	}
}

func TestNormalizeCommandTextShellWrapperCatRedirectWrite(t *testing.T) {
	cmd := normalizeCommandText(`/bin/bash -lc "cat > README.tmp"`, ".", "write file with cat")
	if cmd.RiskLevel != riskLevelGuardedWrite {
		t.Fatalf("expected cat redirect write command to be guarded_write, got %s", cmd.RiskLevel)
	}
}

func TestNormalizeCommandTextShellWrapperDestructive(t *testing.T) {
	cmd := normalizeCommandText(`/bin/bash -lc "rm -rf ."`, ".", "destroy workspace")
	if cmd.RiskLevel != riskLevelDestructive {
		t.Fatalf("expected rm wrapper to be destructive, got %s", cmd.RiskLevel)
	}
	if cmd.Executable != "rm" {
		t.Fatalf("expected unwrapped executable rm, got %s", cmd.Executable)
	}
}

func TestApprovalActionIDFromRequest(t *testing.T) {
	rawID := json.RawMessage("0")

	first := approvalActionIDFromRequest("task_001", "", rawID)
	second := approvalActionIDFromRequest("task_002", "", rawID)
	if first == second {
		t.Fatalf("expected unique ids across task runs, got %q", first)
	}

	withApprovalID := approvalActionIDFromRequest("task_001", "approval-call-1", rawID)
	if withApprovalID == "" {
		t.Fatal("expected approval id to be generated")
	}
	if withApprovalID == first {
		t.Fatalf("expected explicit approval id to affect generated id, got %q", withApprovalID)
	}
}
