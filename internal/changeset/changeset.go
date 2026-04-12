package changeset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Snapshot struct {
	Root string
}

type GeneratedChangeSet struct {
	ID               string
	Summary          string
	ChangedFileCount int
	Files            []File
}

// changeSetSkipExtensions are file extensions excluded from changesets.
var changeSetSkipExtensions = map[string]bool{
	".pyc": true, ".pyo": true, ".class": true,
	".o": true, ".a": true, ".so": true, ".dylib": true,
	".exe": true, ".dll": true,
	".log": true, ".tmp": true,
}

// changeSetSkipNames are exact filenames excluded from changesets.
var changeSetSkipNames = map[string]bool{
	".DS_Store": true, "Thumbs.db": true, "desktop.ini": true,
}

// generatedArtifactDirNames are directory names treated as generated artifacts.
var generatedArtifactDirNames = map[string]bool{
	".git":            true,
	".gemini-session": true,
	"node_modules":    true,
	".next":           true,
	".cache":          true,
	"__pycache__":     true,
	".venv":           true,
	"venv":            true,
	"vendor":          true,
	"dist":            true,
	"build":           true,
	".turbo":          true,
	"target":          true,
	"go-build":        true,
}

// FilterFiles removes noise files (binary caches, logs, OS artifacts)
// from a changeset. Returns nil if no meaningful files remain.
func FilterFiles(files []File) []File {
	var kept []File
	for _, f := range files {
		if isGeneratedArtifactPath(f.Path) {
			continue
		}
		// Skip binary-only diffs with no meaningful content
		if f.Diff != "" && strings.Contains(f.Diff, "Binary files differ") && !strings.Contains(f.Diff, "@@") {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

func shouldSkipWorkspaceDir(dirName string) bool {
	return generatedArtifactDirNames[dirName]
}

func isGeneratedArtifactPath(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" || normalized == "." {
		return false
	}
	segments := strings.Split(normalized, "/")
	for _, segment := range segments {
		if shouldSkipWorkspaceDir(segment) {
			return true
		}
	}
	base := filepath.Base(normalized)
	if changeSetSkipNames[base] {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	if changeSetSkipExtensions[ext] {
		return true
	}
	return false
}

type File struct {
	Path     string `json:"path"`
	Status   string `json:"status"`
	Diff     string `json:"diff,omitempty"`
	Decision string `json:"decision,omitempty"`
}

type FileDecision struct {
	Path     string `json:"path"`
	Decision string `json:"decision"`
}

type FileSnapshot struct {
	Hash string
	Mode fs.FileMode
}

func CreateSnapshot(workspaceRoot string) (Snapshot, error) {
	snapshotRoot, err := os.MkdirTemp("", "pocketcode-workspace-snapshot-")
	if err != nil {
		return Snapshot{}, err
	}
	// Try git-aware snapshot first (only copies tracked + untracked non-ignored files)
	if err := copyGitAwareTree(workspaceRoot, snapshotRoot); err != nil {
		// Fallback to full walk
		if err2 := copyWorkspaceTree(workspaceRoot, snapshotRoot); err2 != nil {
			_ = os.RemoveAll(snapshotRoot)
			return Snapshot{}, err2
		}
	}
	return Snapshot{Root: snapshotRoot}, nil
}

// copyGitAwareTree uses git ls-files to only copy files that git knows about.
func copyGitAwareTree(srcRoot, dstRoot string) error {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = srcRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" {
			continue
		}
		if isGeneratedArtifactPath(rel) {
			continue
		}
		srcPath := filepath.Join(srcRoot, rel)
		info, err := os.Stat(srcPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := copyFile(srcPath, filepath.Join(dstRoot, rel), info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func (s Snapshot) Cleanup() error {
	if strings.TrimSpace(s.Root) == "" {
		return nil
	}
	return os.RemoveAll(s.Root)
}

func (s Snapshot) Restore(workspaceRoot string) error {
	// Collect what files exist in the snapshot vs the current workspace.
	snapshotFiles, err := collectState(s.Root)
	if err != nil {
		return err
	}
	currentFiles, err := collectState(workspaceRoot)
	if err != nil {
		return err
	}

	// Delete files added since snapshot (not in snapshot but in current)
	for rel := range currentFiles {
		if _, inSnapshot := snapshotFiles[rel]; !inSnapshot {
			_ = os.Remove(filepath.Join(workspaceRoot, filepath.FromSlash(rel)))
		}
	}

	// Restore all files from snapshot (overwrites modified, recreates deleted)
	return filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		targetPath := filepath.Join(workspaceRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, targetPath, info.Mode())
	})
}

func BuildChangeSet(taskRunID string, snapshot Snapshot, workspaceRoot string) (*GeneratedChangeSet, error) {
	before, err := collectState(snapshot.Root)
	if err != nil {
		log.Printf("[CHANGESET] taskRun=%s collect snapshot state failed: %v (snapshotRoot=%s)", taskRunID, err, snapshot.Root)
		return nil, err
	}
	after, err := collectState(workspaceRoot)
	if err != nil {
		log.Printf("[CHANGESET] taskRun=%s collect workspace state failed: %v (workspaceRoot=%s)", taskRunID, err, workspaceRoot)
		return nil, err
	}

	files, err := diffWorkspaceState(snapshot.Root, workspaceRoot, before, after)
	if err != nil {
		log.Printf("[CHANGESET] taskRun=%s diff failed: %v", taskRunID, err)
		return nil, err
	}
	rawFiles := files
	files = FilterFiles(files)
	log.Printf("[CHANGESET] taskRun=%s snapshotRoot=%s workspaceRoot=%s beforeN=%d afterN=%d rawDiffN=%d filteredN=%d",
		taskRunID, snapshot.Root, workspaceRoot, len(before), len(after), len(rawFiles), len(files))
	if len(files) == 0 {
		if len(rawFiles) > 0 {
			// Everything got filtered — log the dropped paths so we can see the generated-artifact rules in action.
			dropped := make([]string, 0, len(rawFiles))
			for i, f := range rawFiles {
				if i >= 20 {
					dropped = append(dropped, fmt.Sprintf("...+%d more", len(rawFiles)-20))
					break
				}
				dropped = append(dropped, f.Path)
			}
			log.Printf("[CHANGESET] taskRun=%s all %d candidate files were filtered out: %v", taskRunID, len(rawFiles), dropped)
		}
		return nil, nil
	}

	// Log the file list at debug level so we can confirm what's in the changeset.
	if len(files) <= 20 {
		for _, f := range files {
			log.Printf("[CHANGESET] taskRun=%s file=%s status=%s diffLen=%d", taskRunID, f.Path, f.Status, len(f.Diff))
		}
	} else {
		log.Printf("[CHANGESET] taskRun=%s fileCount=%d (omitted individual file log, over 20 files)", taskRunID, len(files))
	}

	return &GeneratedChangeSet{
		ID:               "cs_" + taskRunID,
		Summary:          SummarizeChangeSet(files),
		ChangedFileCount: len(files),
		Files:            files,
	}, nil
}

func collectState(root string) (map[string]FileSnapshot, error) {
	// Try git-aware collection first (fast path for git repos)
	if state, err := collectGitState(root); err == nil {
		return state, nil
	}

	// Fallback: walk all files, skipping known large dirs
	state := map[string]FileSnapshot{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if shouldSkipWorkspaceDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		state[filepath.ToSlash(rel)] = FileSnapshot{Hash: hash, Mode: info.Mode()}
		return nil
	})
	return state, err
}

func collectGitState(root string) (map[string]FileSnapshot, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	state := map[string]FileSnapshot{}
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" {
			continue
		}
		if isGeneratedArtifactPath(rel) {
			continue
		}
		absPath := filepath.Join(root, rel)
		info, err := os.Stat(absPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		hash, err := fileHash(absPath)
		if err != nil {
			continue
		}
		state[filepath.ToSlash(rel)] = FileSnapshot{Hash: hash, Mode: info.Mode()}
	}
	return state, nil
}

func diffWorkspaceState(beforeRoot, afterRoot string, before, after map[string]FileSnapshot) ([]File, error) {
	seen := map[string]struct{}{}
	files := make([]File, 0)

	for path, prior := range before {
		seen[path] = struct{}{}
		current, ok := after[path]
		if !ok {
			diff, err := BuildFileDiff(beforeRoot, afterRoot, path, "deleted")
			if err != nil {
				return nil, err
			}
			files = append(files, File{Path: path, Status: "deleted", Diff: diff})
			continue
		}
		if prior.Hash != current.Hash {
			diff, err := BuildFileDiff(beforeRoot, afterRoot, path, "modified")
			if err != nil {
				return nil, err
			}
			files = append(files, File{Path: path, Status: "modified", Diff: diff})
		}
	}
	for path := range after {
		if _, ok := seen[path]; ok {
			continue
		}
		diff, err := BuildFileDiff(beforeRoot, afterRoot, path, "added")
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: path, Status: "added", Diff: diff})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Status == files[j].Status {
			return files[i].Path < files[j].Path
		}
		return files[i].Path < files[j].Path
	})

	return files, nil
}

func SummarizeChangeSet(files []File) string {
	if len(files) == 1 {
		return fmt.Sprintf("%s %s", files[0].Path, files[0].Status)
	}
	return fmt.Sprintf("%d files changed", len(files))
}

func ApplySelectiveDecision(snapshot Snapshot, workspaceRoot string, files []File, decisions []FileDecision) error {
	statusByPath := make(map[string]string, len(files))
	for _, file := range files {
		statusByPath[file.Path] = file.Status
	}

	for _, decision := range decisions {
		if decision.Decision != "discard" {
			continue
		}
		status, ok := statusByPath[decision.Path]
		if !ok {
			return fmt.Errorf("unknown file in changeset decision: %s", decision.Path)
		}
		if err := ApplyDiscardForFile(snapshot, workspaceRoot, decision.Path, status); err != nil {
			return err
		}
	}
	return nil
}

func ApplyDiscardForFile(snapshot Snapshot, workspaceRoot, relPath, status string) error {
	cleanRel, err := cleanWorkspaceRelativePath(relPath)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(workspaceRoot, filepath.FromSlash(cleanRel))

	switch status {
	case "added":
		if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	case "modified", "deleted":
		sourcePath := filepath.Join(snapshot.Root, filepath.FromSlash(cleanRel))
		info, err := os.Stat(sourcePath)
		if err != nil {
			return err
		}
		return copyFile(sourcePath, targetPath, info.Mode())
	default:
		return fmt.Errorf("unsupported changeset file status: %s", status)
	}
}

func cleanWorkspaceRelativePath(path string) (string, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return "", fmt.Errorf("changeset path cannot be empty")
	}
	cleaned := filepath.ToSlash(filepath.Clean(normalized))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("invalid changeset path: %s", path)
	}
	return cleaned, nil
}

func BuildFileDiff(beforeRoot, afterRoot, relPath, status string) (string, error) {
	beforeContent, beforeBinary, err := readFileForDiff(filepath.Join(beforeRoot, filepath.FromSlash(relPath)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	afterContent, afterBinary, err := readFileForDiff(filepath.Join(afterRoot, filepath.FromSlash(relPath)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if beforeBinary || afterBinary {
		return fmt.Sprintf("diff --git a/%s b/%s\nBinary files differ\n", relPath, relPath), nil
	}
	return BuildUnifiedTextDiff(relPath, status, beforeContent, afterContent), nil
}

func readFileForDiff(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if len(data) > 0 && bytesContainNUL(data) {
		return "", true, nil
	}

	text := string(data)
	const maxBytes = 1024 * 1024
	if len(text) > maxBytes {
		text = text[:maxBytes] + "\n... diff truncated ..."
	}
	return text, false, nil
}

// editKind represents a diff operation.
type editKind int

const (
	editEqual editKind = iota
	editInsert
	editDelete
)

type editOp struct {
	kind editKind
	line string
}

// computeDiff uses Myers' diff algorithm to produce a minimal edit script.
func computeDiff(a, b []string) []editOp {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	if n == 0 {
		ops := make([]editOp, m)
		for i, l := range b {
			ops[i] = editOp{editInsert, l}
		}
		return ops
	}
	if m == 0 {
		ops := make([]editOp, n)
		for i, l := range a {
			ops[i] = editOp{editDelete, l}
		}
		return ops
	}

	max := n + m
	size := 2*max + 1
	v := make([]int, size)
	trace := make([][]int, 0, max+1)
	found := false

	for d := 0; d <= max; d++ {
		snapshot := make([]int, size)
		copy(snapshot, v)
		trace = append(trace, snapshot)

		for k := -d; k <= d; k += 2 {
			idx := k + max
			var x int
			if k == -d || (k != d && v[idx-1] < v[idx+1]) {
				x = v[idx+1]
			} else {
				x = v[idx-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[idx] = x
			if x >= n && y >= m {
				found = true
			}
		}
		if found {
			after := make([]int, size)
			copy(after, v)
			trace = append(trace, after)
			break
		}
	}

	finalD := len(trace) - 2
	x, y := n, m
	var ops []editOp

	for d := finalD; d >= 0; d-- {
		cur := trace[d+1]
		prev := trace[d]
		k := x - y

		// Determine pre-snake position on diagonal k.
		var startX int
		if d == 0 {
			startX = 0
		} else {
			var prevK int
			if k == -d || (k != d && prev[k-1+max] < prev[k+1+max]) {
				prevK = k + 1
			} else {
				prevK = k - 1
			}
			if prevK == k+1 {
				startX = prev[prevK+max]
			} else {
				startX = prev[prevK+max] + 1
			}
		}
		startY := startX - k
		_ = cur // used to verify trace structure

		// Emit equal lines for the snake.
		for x > startX && y > startY {
			x--
			y--
			ops = append(ops, editOp{editEqual, a[x]})
		}

		// Emit the edit that brought us to this diagonal.
		if d > 0 {
			var prevK int
			if k == -d || (k != d && prev[k-1+max] < prev[k+1+max]) {
				prevK = k + 1
			} else {
				prevK = k - 1
			}
			if prevK == k+1 {
				y--
				ops = append(ops, editOp{editInsert, b[y]})
			} else {
				x--
				ops = append(ops, editOp{editDelete, a[x]})
			}
		}
	}

	// Reverse since we built it backwards.
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// hunk represents a single unified diff hunk.
type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []editOp
}

// buildHunks groups edit ops into hunks with context lines.
func buildHunks(ops []editOp, contextLines int) []hunk {
	if len(ops) == 0 {
		return nil
	}

	// Find ranges of changes, expand by context, merge overlapping.
	type changeRange struct{ start, end int }
	var ranges []changeRange
	for i, op := range ops {
		if op.kind != editEqual {
			s := i - contextLines
			if s < 0 {
				s = 0
			}
			e := i + contextLines + 1
			if e > len(ops) {
				e = len(ops)
			}
			if len(ranges) > 0 && s <= ranges[len(ranges)-1].end {
				ranges[len(ranges)-1].end = e
			} else {
				ranges = append(ranges, changeRange{s, e})
			}
		}
	}

	var hunks []hunk
	for _, r := range ranges {
		h := hunk{}
		// Compute old/new start by counting ops before range.
		oldLine, newLine := 1, 1
		for i := 0; i < r.start; i++ {
			switch ops[i].kind {
			case editEqual:
				oldLine++
				newLine++
			case editDelete:
				oldLine++
			case editInsert:
				newLine++
			}
		}
		h.oldStart = oldLine
		h.newStart = newLine
		for i := r.start; i < r.end; i++ {
			h.lines = append(h.lines, ops[i])
			switch ops[i].kind {
			case editEqual:
				h.oldCount++
				h.newCount++
			case editDelete:
				h.oldCount++
			case editInsert:
				h.newCount++
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

func BuildUnifiedTextDiff(path, status, beforeContent, afterContent string) string {
	beforeLines := splitLines(beforeContent)
	afterLines := splitLines(afterContent)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
	switch status {
	case "added":
		b.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", path))
	case "deleted":
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ /dev/null\n", path))
	default:
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", path, path))
	}

	ops := computeDiff(beforeLines, afterLines)
	hunks := buildHunks(ops, 3)

	for _, h := range hunks {
		b.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount))
		for _, op := range h.lines {
			switch op.kind {
			case editEqual:
				b.WriteString(" ")
			case editDelete:
				b.WriteString("-")
			case editInsert:
				b.WriteString("+")
			}
			b.WriteString(op.line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func splitLines(value string) []string {
	if value == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(value, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func bytesContainNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func copyWorkspaceTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		targetPath := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			if shouldSkipWorkspaceDir(d.Name()) {
				return filepath.SkipDir
			}
			return os.MkdirAll(targetPath, 0o755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		return copyFile(path, targetPath, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
