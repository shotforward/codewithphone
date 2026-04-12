package app

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	runCommandOutputTailLimitBytes = 256 * 1024
	runCommandTerminateGracePeriod = 2 * time.Second
	runCommandLogsDirName          = "pocketcode-command-runs"
	runCommandCacheRootDirName     = "pocketcode-command-cache"
)

type commandExecution struct {
	cmd      *exec.Cmd
	logPath  string
	started  time.Time
	resultCh chan commandExecutionResult
	termOnce sync.Once
}

type commandExecutionResult struct {
	Status          string
	DenyType        string
	ExitCode        int
	Output          string
	OutputTruncated bool
	DurationMs      int64
	WaitErr         error
}

func startCommandExecution(rawCommand, cwd, commandRunID string) (*commandExecution, error) {
	logDir := filepath.Join(os.TempDir(), runCommandLogsDirName)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(logDir, 0o700)
	logPath := filepath.Join(logDir, commandRunID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("/bin/sh", "-c", rawCommand)
	cmd.Dir = cwd
	// Explicitly close stdin so commands that read from stdin (sed, cat,
	// read, etc.) immediately get EOF instead of hanging indefinitely.
	// Go's default is os.DevNull when Stdin is nil, but being explicit
	// protects against edge cases in certain shell wrappers.
	devNull, _ := os.Open(os.DevNull)
	if devNull != nil {
		cmd.Stdin = devNull
		defer devNull.Close()
	}
	commandEnv, err := buildCommandExecutionEnv(commandRunID)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	cmd.Env = commandEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}

	tail := newOutputTailBuffer(runCommandOutputTailLimitBytes)
	sharedWriter := &synchronizedWriter{
		writer: io.MultiWriter(logFile, tail),
	}

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go copyOutput(&copyWG, stdout, sharedWriter)
	go copyOutput(&copyWG, stderr, sharedWriter)

	started := time.Now()
	resultCh := make(chan commandExecutionResult, 1)
	go func() {
		waitErr := cmd.Wait()
		copyWG.Wait()
		_ = logFile.Close()
		resultCh <- buildResult(waitErr, tail, started)
		close(resultCh)
	}()

	return &commandExecution{
		cmd:      cmd,
		logPath:  logPath,
		started:  started,
		resultCh: resultCh,
	}, nil
}

func copyOutput(wg *sync.WaitGroup, reader io.Reader, writer io.Writer) {
	defer wg.Done()
	_, _ = io.Copy(writer, reader)
}

func buildResult(waitErr error, tail *outputTailBuffer, started time.Time) commandExecutionResult {
	exitCode := 0
	status := "success"
	if waitErr != nil {
		status = "failed"
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return commandExecutionResult{
		Status:          status,
		ExitCode:        exitCode,
		Output:          tail.String(),
		OutputTruncated: tail.Truncated(),
		DurationMs:      time.Since(started).Milliseconds(),
		WaitErr:         waitErr,
	}
}

func (e *commandExecution) terminate() {
	if e == nil || e.cmd == nil || e.cmd.Process == nil {
		return
	}
	e.termOnce.Do(func() {
		pid := e.cmd.Process.Pid
		if pid <= 0 {
			return
		}
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		_ = e.cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(runCommandTerminateGracePeriod)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = e.cmd.Process.Kill()
	})
}

func shouldAutoDetachCommand(command string) bool {
	normalized := " " + strings.ToLower(strings.TrimSpace(command)) + " "
	if normalized == "  " {
		return false
	}
	patterns := []string{
		" npm run dev ",
		" npm run start ",
		" pnpm dev ",
		" pnpm start ",
		" yarn dev ",
		" yarn start ",
		" next dev ",
		" next start ",
		" go run ./cmd/server ",
		" go run ./cmd/daemon ",
		" docker compose up ",
		" docker-compose up ",
		" tail -f ",
		" logs -f ",
		" --watch ",
		" webpack serve ",
		" vite ",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

type outputTailBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func newOutputTailBuffer(limit int) *outputTailBuffer {
	if limit <= 0 {
		limit = runCommandOutputTailLimitBytes
	}
	return &outputTailBuffer{
		limit: limit,
		data:  make([]byte, 0, limit),
	}
}

func (b *outputTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.truncated = true
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *outputTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func (b *outputTailBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func buildCommandExecutionEnv(commandRunID string) ([]string, error) {
	baseEnv := envMapFromList(os.Environ())
	safeID := sanitizeCommandRunID(commandRunID)
	cacheRoot := filepath.Join(os.TempDir(), runCommandCacheRootDirName, safeID)

	paths := map[string]string{
		"GOCACHE":             filepath.Join(cacheRoot, "go-build"),
		"GOMODCACHE":          filepath.Join(cacheRoot, "go-mod"),
		"GOTMPDIR":            filepath.Join(cacheRoot, "go-tmp"),
		"TMPDIR":              filepath.Join(cacheRoot, "tmp"),
		"TMP":                 filepath.Join(cacheRoot, "tmp"),
		"TEMP":                filepath.Join(cacheRoot, "tmp"),
		"XDG_CACHE_HOME":      filepath.Join(cacheRoot, "xdg-cache"),
		"PYTHONPYCACHEPREFIX": filepath.Join(cacheRoot, "python-pyc"),
		"PIP_CACHE_DIR":       filepath.Join(cacheRoot, "pip-cache"),
		"NPM_CONFIG_CACHE":    filepath.Join(cacheRoot, "npm-cache"),
		"YARN_CACHE_FOLDER":   filepath.Join(cacheRoot, "yarn-cache"),
	}
	for _, dir := range paths {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(dir, 0o700)
	}
	for key, value := range paths {
		baseEnv[key] = value
	}

	return envListFromMap(baseEnv), nil
}

func sanitizeCommandRunID(commandRunID string) string {
	trimmed := strings.TrimSpace(commandRunID)
	if trimmed == "" {
		return "cmd"
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	safe := strings.Trim(b.String(), "._-")
	if safe == "" {
		return "cmd"
	}
	return safe
}

func envMapFromList(values []string) map[string]string {
	envMap := make(map[string]string, len(values))
	for _, item := range values {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		envMap[parts[0]] = parts[1]
	}
	return envMap
}

func envListFromMap(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	envList := make([]string, 0, len(keys))
	for _, key := range keys {
		envList = append(envList, key+"="+values[key])
	}
	return envList
}
