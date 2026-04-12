package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type daemonMachineDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type daemonMachineDirectoryListResponse struct {
	Path         string                        `json:"path"`
	ParentPath   string                        `json:"parentPath,omitempty"`
	AllowedRoots []string                      `json:"allowedRoots,omitempty"`
	Items        []daemonMachineDirectoryEntry `json:"items"`
	HasMore      bool                          `json:"hasMore,omitempty"`
	NextCursor   string                        `json:"nextCursor,omitempty"`
}

type daemonWorkspaceFileEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
}

type daemonWorkspaceFileSearchResponse struct {
	Items []daemonWorkspaceFileEntry `json:"items"`
}

type daemonWorkspaceFilePreviewResponse struct {
	Path       string `json:"path"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated"`
}

type daemonWorkspaceEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type daemonWorkspaceEntryListResponse struct {
	Path       string                 `json:"path"`
	ParentPath string                 `json:"parentPath,omitempty"`
	Items      []daemonWorkspaceEntry `json:"items"`
	HasMore    bool                   `json:"hasMore,omitempty"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type daemonListDirectoriesRequest struct {
	Path   string `json:"path"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}

type daemonMakeDirectoryRequest struct {
	Path string `json:"path"`
}

type daemonMakeDirectoryResponse struct {
	Path string `json:"path"`
}

type daemonListFilesRequest struct {
	WorkspaceRoot string `json:"workspaceRoot"`
	Query         string `json:"query"`
	Limit         int    `json:"limit"`
}

type daemonPreviewFileRequest struct {
	WorkspaceRoot string `json:"workspaceRoot"`
	Path          string `json:"path"`
	StartLine     int    `json:"startLine"`
	EndLine       int    `json:"endLine"`
}

type daemonListWorkspaceEntriesRequest struct {
	WorkspaceRoot string `json:"workspaceRoot"`
	Path          string `json:"path,omitempty"`
	Limit         int    `json:"limit"`
	Cursor        string `json:"cursor,omitempty"`
}

type daemonTerminateCommandRequest struct {
	CommandRunID string `json:"commandRunId"`
	Reason       string `json:"reason"`
}

type daemonTerminateCommandResponse struct {
	CommandRunID string `json:"commandRunId"`
	PID          int    `json:"pid"`
	Status       string `json:"status"`
}

func (s *Service) runFSTaskLoop(ctx context.Context) error {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		dispatch, err := s.serverClient.claimFSTask(ctx)
		if err != nil {
			log.Printf("claim fs task failed: %v", err)
		} else if dispatch != nil {
			if err := s.handleFSTaskDispatch(ctx, *dispatch); err != nil {
				log.Printf("handle fs dispatch %s failed: %v", dispatch.TaskID, err)
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) handleFSTaskDispatch(ctx context.Context, dispatch fsTaskDispatch) error {
	switch strings.TrimSpace(dispatch.TaskType) {
	case "list_directories":
		var req daemonListDirectoriesRequest
		if err := json.Unmarshal(dispatch.RequestJSON, &req); err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "invalid list_directories payload", nil)
		}
		response, err := s.executeListDirectories(req.Path, req.Limit, req.Cursor)
		if err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", err.Error(), nil)
		}
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "completed", "", response)
	case "list_files":
		var req daemonListFilesRequest
		if err := json.Unmarshal(dispatch.RequestJSON, &req); err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "invalid list_files payload", nil)
		}
		items, err := s.executeListFiles(req.WorkspaceRoot, req.Query, req.Limit)
		if err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", err.Error(), nil)
		}
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "completed", "", daemonWorkspaceFileSearchResponse{Items: items})
	case "preview_file":
		var req daemonPreviewFileRequest
		if err := json.Unmarshal(dispatch.RequestJSON, &req); err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "invalid preview_file payload", nil)
		}
		response, err := s.executePreviewFile(req)
		if err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", err.Error(), nil)
		}
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "completed", "", response)
	case "list_workspace_entries":
		var req daemonListWorkspaceEntriesRequest
		if err := json.Unmarshal(dispatch.RequestJSON, &req); err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "invalid list_workspace_entries payload", nil)
		}
		response, err := s.executeListWorkspaceEntries(req.WorkspaceRoot, req.Path, req.Limit, req.Cursor)
		if err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", err.Error(), nil)
		}
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "completed", "", response)
	case "mkdir":
		var req daemonMakeDirectoryRequest
		if err := json.Unmarshal(dispatch.RequestJSON, &req); err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "invalid mkdir payload", nil)
		}
		response, err := s.executeMakeDirectory(req.Path)
		if err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", err.Error(), nil)
		}
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "completed", "", response)
	case "terminate_command":
		var req daemonTerminateCommandRequest
		if err := json.Unmarshal(dispatch.RequestJSON, &req); err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "invalid terminate_command payload", nil)
		}
		response, err := s.executeTerminateCommand(dispatch.SessionID, req.CommandRunID, req.Reason)
		if err != nil {
			return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", err.Error(), nil)
		}
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "completed", "", response)
	default:
		return s.serverClient.completeFSTask(ctx, dispatch.TaskID, "failed", "unsupported fs task type", nil)
	}
}

func (s *Service) currentAllowedRoots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.allowedRoots) == 0 {
		return resolveAllowedRoots()
	}
	roots := make([]string, len(s.allowedRoots))
	copy(roots, s.allowedRoots)
	return roots
}

func (s *Service) executeListDirectories(requestedPath string, limit int, cursor string) (daemonMachineDirectoryListResponse, error) {
	allowedRoots := s.currentAllowedRoots()
	if len(allowedRoots) == 0 {
		return daemonMachineDirectoryListResponse{}, errors.New("machine has no allowed roots")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	if strings.TrimSpace(requestedPath) == "" {
		items := make([]daemonMachineDirectoryEntry, 0, len(allowedRoots))
		for _, root := range allowedRoots {
			items = append(items, daemonMachineDirectoryEntry{Name: root, Path: root, Kind: "directory"})
		}
		return daemonMachineDirectoryListResponse{
			Path:         "",
			AllowedRoots: allowedRoots,
			Items:        items,
		}, nil
	}

	targetPath, err := filepath.Abs(strings.TrimSpace(requestedPath))
	if err != nil {
		return daemonMachineDirectoryListResponse{}, errors.New("path must be absolute")
	}
	if !daemonPathWithinAnyRoot(targetPath, allowedRoots) {
		return daemonMachineDirectoryListResponse{}, errors.New("path is outside allowed roots")
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return daemonMachineDirectoryListResponse{}, errors.New("path not found")
		}
		return daemonMachineDirectoryListResponse{}, err
	}
	if !info.IsDir() {
		return daemonMachineDirectoryListResponse{}, errors.New("path must be a directory")
	}

	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return daemonMachineDirectoryListResponse{}, err
	}
	items := make([]daemonMachineDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		itemPath := filepath.Join(targetPath, entry.Name())
		itemKind := daemonDirEntryKind(itemPath, entry)
		items = append(items, daemonMachineDirectoryEntry{
			Name: entry.Name(),
			Path: itemPath,
			Kind: itemKind,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == "directory"
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	start := 0
	if cursor != "" {
		for i, item := range items {
			if item.Path == cursor {
				start = i + 1
				break
			}
		}
		if start > len(items) {
			start = len(items)
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	hasMore := end < len(items)
	items = items[start:end]

	parentPath := ""
	candidateParent := filepath.Dir(targetPath)
	if candidateParent != targetPath && daemonPathWithinAnyRoot(candidateParent, allowedRoots) {
		parentPath = candidateParent
	}
	return daemonMachineDirectoryListResponse{
		Path:         targetPath,
		ParentPath:   parentPath,
		AllowedRoots: allowedRoots,
		Items:        items,
		HasMore:      hasMore,
		NextCursor: func() string {
			if hasMore && len(items) > 0 {
				return items[len(items)-1].Path
			}
			return ""
		}(),
	}, nil
}

func (s *Service) executeMakeDirectory(requestedPath string) (daemonMakeDirectoryResponse, error) {
	allowedRoots := s.currentAllowedRoots()
	if len(allowedRoots) == 0 {
		return daemonMakeDirectoryResponse{}, errors.New("machine has no allowed roots")
	}

	trimmed := strings.TrimSpace(requestedPath)
	if trimmed == "" {
		return daemonMakeDirectoryResponse{}, errors.New("path is required")
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return daemonMakeDirectoryResponse{}, errors.New("path must be absolute")
	}
	if !daemonPathWithinAnyRoot(absPath, allowedRoots) {
		return daemonMakeDirectoryResponse{}, errors.New("path is outside allowed roots")
	}
	if err := os.MkdirAll(absPath, 0o755); err != nil {
		return daemonMakeDirectoryResponse{}, err
	}
	return daemonMakeDirectoryResponse{Path: absPath}, nil
}

func (s *Service) executeTerminateCommand(sessionID, commandRunID, _ string) (daemonTerminateCommandResponse, error) {
	trimmedID := strings.TrimSpace(commandRunID)
	if trimmedID == "" {
		return daemonTerminateCommandResponse{}, errors.New("commandRunId is required")
	}
	run, ok := s.terminateRunningCommand(sessionID, trimmedID)
	if !ok {
		return daemonTerminateCommandResponse{
			CommandRunID: trimmedID,
			PID:          0,
			Status:       "not_found",
		}, nil
	}
	return daemonTerminateCommandResponse{
		CommandRunID: run.CommandRunID,
		PID:          run.PID,
		Status:       "terminated",
	}, nil
}

func (s *Service) executeListFiles(workspaceRoot, query string, limit int) ([]daemonWorkspaceFileEntry, error) {
	absWorkspaceRoot, err := s.validateWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > 200 {
		limit = 200
	}
	return listDaemonWorkspaceFiles(absWorkspaceRoot, query, limit)
}

func (s *Service) executePreviewFile(req daemonPreviewFileRequest) (daemonWorkspaceFilePreviewResponse, error) {
	absWorkspaceRoot, err := s.validateWorkspaceRoot(req.WorkspaceRoot)
	if err != nil {
		return daemonWorkspaceFilePreviewResponse{}, err
	}
	relPath, err := daemonSanitizeRelativePath(req.Path)
	if err != nil {
		return daemonWorkspaceFilePreviewResponse{}, err
	}
	absFilePath, err := daemonResolveWorkspacePath(absWorkspaceRoot, relPath)
	if err != nil {
		return daemonWorkspaceFilePreviewResponse{}, err
	}
	startLine := req.StartLine
	if startLine <= 0 {
		startLine = 1
	}
	endLine := req.EndLine
	if endLine < startLine {
		endLine = startLine
	}
	content, totalLines, truncated, err := daemonPreviewTextFile(absFilePath, startLine, endLine)
	if err != nil {
		return daemonWorkspaceFilePreviewResponse{}, err
	}
	return daemonWorkspaceFilePreviewResponse{
		Path:       filepath.ToSlash(relPath),
		StartLine:  startLine,
		EndLine:    endLine,
		TotalLines: totalLines,
		Content:    content,
		Truncated:  truncated,
	}, nil
}

func (s *Service) executeListWorkspaceEntries(workspaceRoot, requestedPath string, limit int, cursor string) (daemonWorkspaceEntryListResponse, error) {
	absWorkspaceRoot, err := s.validateWorkspaceRoot(workspaceRoot)
	if err != nil {
		return daemonWorkspaceEntryListResponse{}, err
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}

	relativePath := ""
	trimmedPath := strings.TrimSpace(requestedPath)
	if trimmedPath != "" {
		relativePath, err = daemonSanitizeRelativePath(trimmedPath)
		if err != nil {
			return daemonWorkspaceEntryListResponse{}, err
		}
	}

	targetPath := absWorkspaceRoot
	if relativePath != "" {
		targetPath, err = daemonResolveWorkspacePath(absWorkspaceRoot, relativePath)
		if err != nil {
			return daemonWorkspaceEntryListResponse{}, err
		}
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return daemonWorkspaceEntryListResponse{}, errors.New("path not found")
		}
		return daemonWorkspaceEntryListResponse{}, err
	}
	if !info.IsDir() {
		return daemonWorkspaceEntryListResponse{}, errors.New("path must be a directory")
	}

	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return daemonWorkspaceEntryListResponse{}, err
	}
	items := make([]daemonWorkspaceEntry, 0, len(entries))
	for _, entry := range entries {
		itemPath := filepath.Join(targetPath, entry.Name())
		relativeItemPath, err := filepath.Rel(absWorkspaceRoot, itemPath)
		if err != nil {
			continue
		}
		kind := daemonDirEntryKind(itemPath, entry)
		items = append(items, daemonWorkspaceEntry{
			Name: entry.Name(),
			Path: filepath.ToSlash(relativeItemPath),
			Kind: kind,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == "directory"
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	start := 0
	if cursor != "" {
		for i, item := range items {
			if item.Path == cursor {
				start = i + 1
				break
			}
		}
		if start > len(items) {
			start = len(items)
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	hasMore := end < len(items)
	items = items[start:end]

	parentPath := ""
	if relativePath != "" {
		candidateParent := filepath.Dir(relativePath)
		if candidateParent != "." {
			parentPath = filepath.ToSlash(candidateParent)
		}
	}

	currentPath := filepath.ToSlash(relativePath)
	if currentPath == "." {
		currentPath = ""
	}
	return daemonWorkspaceEntryListResponse{
		Path:       currentPath,
		ParentPath: parentPath,
		Items:      items,
		HasMore:    hasMore,
		NextCursor: func() string {
			if hasMore && len(items) > 0 {
				return items[len(items)-1].Path
			}
			return ""
		}(),
	}, nil
}

func (s *Service) validateWorkspaceRoot(workspaceRoot string) (string, error) {
	trimmed := strings.TrimSpace(workspaceRoot)
	if trimmed == "" {
		return "", errors.New("workspace root is required")
	}
	absWorkspaceRoot, err := filepath.Abs(trimmed)
	if err != nil {
		return "", errors.New("workspace root is invalid")
	}
	allowedRoots := s.currentAllowedRoots()
	if len(allowedRoots) > 0 && !daemonPathWithinAnyRoot(absWorkspaceRoot, allowedRoots) {
		return "", errors.New("workspace root is outside allowed roots")
	}
	info, err := os.Stat(absWorkspaceRoot)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workspace root is not a directory")
	}
	return absWorkspaceRoot, nil
}

func listDaemonWorkspaceFiles(workspaceRoot, query string, limit int) ([]daemonWorkspaceFileEntry, error) {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	type scoredPath struct {
		path      string
		score     int
		sizeBytes int64
	}
	matches := make([]scoredPath, 0, limit)
	seen := 0
	maxScan := 8000
	skipDirs := map[string]struct{}{
		".git":         {},
		"node_modules": {},
		".next":        {},
		"dist":         {},
		"build":        {},
		".cache":       {},
	}

	walkErr := filepath.WalkDir(workspaceRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, blocked := skipDirs[d.Name()]; blocked {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > maxScan {
			return errors.New("workspace has too many files to index")
		}
		relPath, err := filepath.Rel(workspaceRoot, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		score := 2
		if lowerQuery != "" {
			lowerPath := strings.ToLower(relPath)
			if strings.HasPrefix(lowerPath, lowerQuery) {
				score = 0
			} else if strings.Contains(lowerPath, lowerQuery) {
				score = 1
			} else {
				return nil
			}
		} else {
			score = 1
		}
		var sizeBytes int64
		if info, err := d.Info(); err == nil {
			sizeBytes = info.Size()
		}
		matches = append(matches, scoredPath{path: relPath, score: score, sizeBytes: sizeBytes})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].path < matches[j].path
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	result := make([]daemonWorkspaceFileEntry, 0, len(matches))
	for _, match := range matches {
		result = append(result, daemonWorkspaceFileEntry{Path: match.path, SizeBytes: match.sizeBytes})
	}
	return result, nil
}

func daemonSanitizeRelativePath(rawPath string) (string, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(rawPath, "\\", "/"))
	if normalized == "" {
		return "", errors.New("path is required")
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." {
		return "", errors.New("path must target a file")
	}
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must stay inside workspace")
	}
	return filepath.FromSlash(cleaned), nil
}

func daemonResolveWorkspacePath(workspaceRoot, relPath string) (string, error) {
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	absFilePath := filepath.Join(absRoot, relPath)
	absFilePath, err = filepath.Abs(absFilePath)
	if err != nil {
		return "", err
	}
	if absFilePath != absRoot && !strings.HasPrefix(absFilePath, absRoot+string(os.PathSeparator)) {
		return "", errors.New("path escapes workspace root")
	}
	return absFilePath, nil
}

func daemonPreviewTextFile(absFilePath string, startLine, endLine int) (string, int, bool, error) {
	fileInfo, err := os.Stat(absFilePath)
	if err != nil {
		return "", 0, false, err
	}
	if fileInfo.IsDir() {
		return "", 0, false, errors.New("path points to a directory")
	}

	const maxReadBytes = 512 * 1024
	payload, err := os.ReadFile(absFilePath)
	if err != nil {
		return "", 0, false, err
	}
	if len(payload) > maxReadBytes {
		payload = payload[:maxReadBytes]
	}
	if len(payload) > 0 && strings.IndexByte(string(payload[:daemonMinInt(len(payload), 4096)]), 0) >= 0 {
		return "", 0, false, errors.New("binary file is not previewable")
	}

	lines := strings.Split(string(payload), "\n")
	totalLines := len(lines)
	if totalLines == 0 {
		totalLines = 1
		lines = []string{""}
	}
	if startLine > totalLines {
		startLine = totalLines
	}
	if endLine > totalLines {
		endLine = totalLines
	}
	if endLine < startLine {
		endLine = startLine
	}
	segment := strings.Join(lines[startLine-1:endLine], "\n")
	truncated := fileInfo.Size() > int64(len(payload)) || endLine < totalLines
	return segment, totalLines, truncated, nil
}

func daemonMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func daemonPathWithinRoot(absPath, absRoot string) bool {
	absPath = filepath.Clean(absPath)
	absRoot = filepath.Clean(absRoot)
	return absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(os.PathSeparator))
}

func daemonPathWithinAnyRoot(absPath string, allowedRoots []string) bool {
	for _, root := range allowedRoots {
		if daemonPathWithinRoot(absPath, root) {
			return true
		}
	}
	return false
}

func daemonDirEntryKind(absPath string, entry fs.DirEntry) string {
	if entry.IsDir() {
		return "directory"
	}
	// os.ReadDir marks symlinks as non-directories; follow target to preserve folder browsing UX.
	info, err := os.Stat(absPath)
	if err == nil && info.IsDir() {
		return "directory"
	}
	return "file"
}
