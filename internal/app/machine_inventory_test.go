package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAllowedRootsDefaultsToUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DAEMON_ALLOWED_ROOTS", "")

	roots := resolveAllowedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected one default root, got %v", roots)
	}
	if roots[0] != home {
		t.Fatalf("expected home root %q, got %q", home, roots[0])
	}
}

func TestPreferredWorkspaceRootUsesHomeWhenAllowed(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	other := filepath.Join(base, "workspace")
	mkdirAll(t, home)
	mkdirAll(t, other)
	t.Setenv("HOME", home)

	roots := normalizeRoots([]string{other, home})
	if got := preferredWorkspaceRoot(roots); got != home {
		t.Fatalf("expected preferred root %q, got %q from %v", home, got, roots)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
