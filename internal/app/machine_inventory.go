package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func resolveAllowedRoots() []string {
	if raw := strings.TrimSpace(os.Getenv("DAEMON_ALLOWED_ROOTS")); raw != "" {
		roots := normalizeRoots(strings.Split(raw, string(os.PathListSeparator)))
		if len(roots) > 0 {
			return roots
		}
	}

	home := strings.TrimSpace(os.Getenv("HOME"))
	if home != "" {
		if roots := normalizeRoots([]string{home}); len(roots) > 0 {
			return roots
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		if roots := normalizeRoots([]string{cwd}); len(roots) > 0 {
			return roots
		}
	}

	return []string{"."}
}

func normalizeRoots(inputs []string) []string {
	seen := map[string]struct{}{}
	roots := make([]string, 0, len(inputs))
	for _, input := range inputs {
		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			continue
		}
		abs, err := filepath.Abs(trimmed)
		if err != nil {
			continue
		}
		// Resolve symlinks so that paths like /tmp (→ /private/tmp on
		// macOS) match correctly against the canonical allowed root.
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		roots = append(roots, abs)
	}
	sort.Strings(roots)
	return roots
}

func discoverGitProjects(roots []string, limit int) []string {
	if limit <= 0 {
		limit = 200
	}
	seen := map[string]struct{}{}
	projects := make([]string, 0, limit)

	for _, root := range roots {
		if len(projects) >= limit {
			break
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if len(projects) >= limit {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				return nil
			}
			base := d.Name()
			if base == ".git" {
				return filepath.SkipDir
			}
			if base == "node_modules" || base == ".next" {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(root, path)
			if err == nil && rel != "." && strings.Count(rel, string(os.PathSeparator)) >= 5 {
				return filepath.SkipDir
			}
			if hasGitMarker(path) {
				if _, ok := seen[path]; !ok {
					seen[path] = struct{}{}
					projects = append(projects, path)
				}
				return filepath.SkipDir
			}
			return nil
		})
	}

	sort.Strings(projects)
	return projects
}

func hasGitMarker(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}
