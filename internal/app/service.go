package app

import (
	"github.com/shotforward/codewithphone/internal/changeset"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shotforward/codewithphone/internal/config"
)

// Option configures a Service.
type Option func(*Service)

// WithDaemonMode marks the service as running in background (non-interactive).
func WithDaemonMode() Option {
	return func(s *Service) { s.interactive = false }
}

const defaultPollInterval = 100 * time.Millisecond
const changeSetWaitTimeout = 30 * time.Minute
const sessionTerminationPollInterval = 1 * time.Second
const tokenValidationTimeout = 8 * time.Second
const machineOfflineNotifyTimeout = 2 * time.Second

type turnRunner interface {
	RunTurn(ctx context.Context, dispatch taskDispatch, providerSessionRef string, profile turnExecutionProfile) (string, error)
}

// pendingChangeSet tracks an in-flight changeset whose decision is being waited
// on by a background goroutine. When a new turn starts for the same session,
// the previous pending changeset is auto-kept so it doesn't block.
type pendingChangeSet struct {
	changeSetID string
	sessionID   string
	taskRunID   string
	snapshot    changeset.Snapshot
	cancel      context.CancelFunc
}

type Service struct {
	cfg         config.Config
	interactive bool // true when running in foreground with a terminal

	mu                sync.Mutex
	actualAddr        string // resolved listen address (useful when port=0)
	providerSessions  map[string]string
	sessionWorkspaces map[string]string // sessionID -> first workspaceRoot
	taskWorkspaces         map[string]string // taskRunID -> workspaceRoot
	taskWorkspaceSnapshots map[string]string // taskRunID -> snapshot.Root for "vs turn start" diffs
	taskProfiles           map[string]turnExecutionProfile
	deniedApprovals   map[string]map[string]approvalStatus // taskRunID -> command fingerprint -> latest denied status
	pendingSnapshots  map[string]*pendingChangeSet         // sessionID -> pending
	allowedRoots      []string
	capabilities      runtimeCapabilitiesPayload
	serverClient      serverClient
	codexRunner       turnRunner
	geminiRunner      turnRunner
	claudeRunner      turnRunner
	changeSets        changeSetClient
	pollInterval      time.Duration

	backgroundMu       sync.Mutex
	backgroundCommands map[string]backgroundCommandRun

	runningCommandsMu sync.Mutex
	runningCommands   map[string]runningCommand
}

// ActualAddr returns the resolved listen address after the server starts.
func (s *Service) ActualAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actualAddr
}

// EnsureBinding runs the device auth / binding flow interactively (foreground).
// After a successful binding the config (with machine_token) is saved.
// This is meant to be called before daemonizing so the user can approve the
// PIN on their terminal.
func (s *Service) EnsureBinding(ctx context.Context) error {
	hostname, _ := os.Hostname()
	return s.ensureMachineBinding(ctx, hostname, machineInventory{})
}

// Config returns the current (possibly mutated) config — useful for reading
// the machine token after a successful binding.
func (s *Service) Config() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

type healthResponse struct {
	Status    string `json:"status"`
	MachineID string `json:"machineId"`
	Now       string `json:"now"`
}

type runtimeStateResponse struct {
	MachineID     string `json:"machineId"`
	SQLitePath    string `json:"sqlitePath"`
	ServerBaseURL string `json:"serverBaseUrl"`
	ServerWSURL   string `json:"serverWsUrl"`
}

func New(cfg config.Config, opts ...Option) *Service {
	client := serverClient{
		BaseURL:      cfg.ServerBaseURL,
		MachineID:    cfg.MachineID,
		MachineToken: cfg.MachineToken,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}

	svc := &Service{
		cfg:               cfg,
		interactive:       true, // default: foreground with terminal
		providerSessions:  map[string]string{},
		sessionWorkspaces: map[string]string{},
		taskWorkspaces:         map[string]string{},
		taskWorkspaceSnapshots: map[string]string{},
		taskProfiles:           map[string]turnExecutionProfile{},
		deniedApprovals:   map[string]map[string]approvalStatus{},
		pendingSnapshots:  map[string]*pendingChangeSet{},
		capabilities:      defaultRuntimeCapabilities(cfg),
		serverClient:      client,
		changeSets: changeSetClient{
			BaseURL:      cfg.ServerBaseURL,
			HTTPClient:   client.httpClient(),
			PollInterval: 500 * time.Millisecond,
		},
		pollInterval:          defaultPollInterval,
		backgroundCommands:    map[string]backgroundCommandRun{},
		runningCommands:       map[string]runningCommand{},
	}
	for _, opt := range opts {
		opt(svc)
	}
	resolveBaseURL := func() string {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		if svc.actualAddr != "" {
			return daemonBaseURLFromHTTPAddr(svc.actualAddr)
		}
		return ""
	}
	svc.codexRunner = newCodexRunner(cfg, &svc.serverClient)
	svc.geminiRunner = newGeminiRunner(cfg, &svc.serverClient, resolveBaseURL)
	svc.claudeRunner = newClaudeRunner(cfg, &svc.serverClient, resolveBaseURL)
	return svc
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.ensureDataDir(); err != nil {
		return fmt.Errorf("prepare daemon data directory: %w", err)
	}

	// Log CLI tool versions at startup so operators can see at a glance
	// whether any runner is out of the tested range.
	CheckRunnerVersions(map[string]string{
		"codex":  s.cfg.CodexBin,
		"claude": s.cfg.ClaudeBin,
		"gemini": s.cfg.GeminiBin,
	})

	hostname, _ := os.Hostname()
	allowedRoots := resolveAllowedRoots()
	s.mu.Lock()
	s.allowedRoots = append([]string(nil), allowedRoots...)
	s.mu.Unlock()
	workspaceRoot := ""
	if len(allowedRoots) > 0 {
		workspaceRoot = allowedRoots[0]
	}
	if workspaceRoot == "" {
		workspaceRoot, _ = os.Getwd()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/runtime/state", s.handleRuntimeState)
	mux.HandleFunc("POST /internal/mcp/tool_call", s.handleMCPToolCall)
	mux.HandleFunc("GET /mcp/sse", s.handleMCPSSE)
	mux.HandleFunc("POST /mcp/message", s.handleMCPMessage)

	// Bind listener first so we know the actual port (supports :0 for auto-select).
	ln, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.HTTPAddr, err)
	}
	actualAddr := ln.Addr().String()
	s.mu.Lock()
	s.actualAddr = actualAddr
	s.mu.Unlock()
	log.Printf("codewithphone listening on %s", actualAddr)

	server := &http.Server{
		Handler: mux,
	}

	errCh := make(chan error, 3)
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go s.refreshRuntimeCapabilities(context.Background())

	if err := s.ensureMachineBinding(ctx, hostname, machineInventory{
		AllowedRoots: allowedRoots,
		Capabilities: s.getRuntimeCapabilities(),
	}); err != nil {
		return err
	}

	regCtx, regCancel := context.WithTimeout(ctx, 10*time.Second)
	var registered atomic.Bool
	var startupRecovered atomic.Bool
	recoverStartupTasks := func() {
		if !startupRecovered.CompareAndSwap(false, true) {
			return
		}
		recoverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		recovered, err := s.serverClient.recoverActiveTasks(recoverCtx)
		if err != nil {
			log.Printf("startup recovery failed: %v", err)
			return
		}
		if recovered > 0 {
			log.Printf("startup recovered stale active tasks: count=%d machine=%s", recovered, s.serverClient.MachineID)
		}
	}
	registerMachine := func() bool {
		regCtx, regCancel := context.WithTimeout(ctx, 10*time.Second)
		defer regCancel()
		if err := s.serverClient.registerMachine(regCtx, "", hostname, "0.1.0", workspaceRoot, machineInventory{
			AllowedRoots: allowedRoots,
			Capabilities: s.getRuntimeCapabilities(),
		}); err != nil {
			return false
		}
		recoverStartupTasks()
		return true
	}
	if err := s.serverClient.registerMachine(regCtx, "", hostname, "0.1.0", workspaceRoot, machineInventory{
		AllowedRoots: allowedRoots,
		Capabilities: s.getRuntimeCapabilities(),
	}); err != nil {
		log.Printf("WARNING: failed to register with server: %v", err)
	} else {
		registered.Store(true)
		recoverStartupTasks()
	}
	regCancel()

	// If startup registration races server readiness, retry quickly until success.
	go func() {
		if registered.Load() {
			return
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if registerMachine() {
					registered.Store(true)
					return
				}
			}
		}
	}()

	// Start heartbeat goroutine. Send one immediately so the machine appears online
	// right after startup without waiting for the first 30-second tick.
	go func() {
		initialInventory := machineInventory{
			AllowedRoots: allowedRoots,
			Projects:     discoverGitProjects(allowedRoots, 200),
			Capabilities: s.getRuntimeCapabilities(),
		}
		if err := s.serverClient.heartbeat(ctx, initialInventory); err != nil {
			log.Printf("initial heartbeat failed: %v", err)
		}

		ticker := time.NewTicker(30 * time.Second)
		capabilityTicker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		defer capabilityTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-capabilityTicker.C:
				s.refreshRuntimeCapabilities(context.Background())
			case <-ticker.C:
				inventory := machineInventory{
					AllowedRoots: allowedRoots,
					Projects:     discoverGitProjects(allowedRoots, 200),
					Capabilities: s.getRuntimeCapabilities(),
				}
				if err := s.serverClient.heartbeat(ctx, inventory); err != nil {
					// Registration can race server startup; retry lightweight register on heartbeat failure.
					if registerMachine() {
						registered.Store(true)
					}
					log.Printf("heartbeat failed: %v", err)
				}
			}
		}
	}()

	go func() {
		errCh <- s.runTaskLoop(ctx)
	}()
	go func() {
		errCh <- s.runFSTaskLoop(ctx)
	}()

	var notifyOfflineOnce sync.Once
	notifyOffline := func() {
		notifyOfflineOnce.Do(func() {
			s.notifyMachineOffline()
		})
	}

	select {
	case <-ctx.Done():
		notifyOffline()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		notifyOffline()
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = server.Shutdown(context.Background())
			return err
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func (s *Service) getProviderSession(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providerSessions[sessionID]
}

func (s *Service) setProviderSession(sessionID, providerSessionRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerSessions[sessionID] = providerSessionRef
}

// pinSessionWorkspace records the first workspaceRoot used for a session and
// always returns that same path, so Gemini CLI --resume finds the right project.
func (s *Service) pinSessionWorkspace(sessionID, workspaceRoot string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessionWorkspaces[sessionID]; ok {
		return existing
	}
	s.sessionWorkspaces[sessionID] = workspaceRoot
	return workspaceRoot
}

func (s *Service) setTaskWorkspace(taskRunID, workspaceRoot string) {
	taskRunID = strings.TrimSpace(taskRunID)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if taskRunID == "" || workspaceRoot == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskWorkspaces == nil {
		s.taskWorkspaces = map[string]string{}
	}
	s.taskWorkspaces[taskRunID] = workspaceRoot
}

func (s *Service) setTaskWorkspaceSnapshot(taskRunID, snapshotRoot string) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskWorkspaceSnapshots == nil {
		s.taskWorkspaceSnapshots = map[string]string{}
	}
	s.taskWorkspaceSnapshots[taskRunID] = snapshotRoot
}

func (s *Service) clearTaskWorkspaceSnapshot(taskRunID string) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.taskWorkspaceSnapshots, taskRunID)
}

func (s *Service) getTaskWorkspaceSnapshot(taskRunID string) string {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskWorkspaceSnapshots[taskRunID]
}

func (s *Service) setTaskProfile(taskRunID string, profile turnExecutionProfile) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskProfiles == nil {
		s.taskProfiles = map[string]turnExecutionProfile{}
	}
	s.taskProfiles[taskRunID] = profile
}

func (s *Service) getTaskWorkspace(taskRunID string) string {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskWorkspaces[taskRunID]
}

func (s *Service) getTaskProfile(taskRunID string) (turnExecutionProfile, bool) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return turnExecutionProfile{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.taskProfiles[taskRunID]
	return profile, ok
}

func (s *Service) clearTaskWorkspace(taskRunID string) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.taskWorkspaces, taskRunID)
}

func (s *Service) clearTaskProfile(taskRunID string) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.taskProfiles, taskRunID)
}

func (s *Service) rememberTaskDeniedApproval(taskRunID, fingerprint string, status approvalStatus) {
	taskRunID = strings.TrimSpace(taskRunID)
	fingerprint = strings.TrimSpace(fingerprint)
	if taskRunID == "" || fingerprint == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deniedApprovals == nil {
		s.deniedApprovals = map[string]map[string]approvalStatus{}
	}
	perTask := s.deniedApprovals[taskRunID]
	if perTask == nil {
		perTask = map[string]approvalStatus{}
		s.deniedApprovals[taskRunID] = perTask
	}
	perTask[fingerprint] = status
}

func (s *Service) getTaskDeniedApproval(taskRunID, fingerprint string) (approvalStatus, bool) {
	taskRunID = strings.TrimSpace(taskRunID)
	fingerprint = strings.TrimSpace(fingerprint)
	if taskRunID == "" || fingerprint == "" {
		return approvalStatus{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	perTask := s.deniedApprovals[taskRunID]
	if perTask == nil {
		return approvalStatus{}, false
	}
	status, ok := perTask[fingerprint]
	return status, ok
}

func (s *Service) clearTaskDeniedApprovals(taskRunID string) {
	taskRunID = strings.TrimSpace(taskRunID)
	if taskRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deniedApprovals, taskRunID)
}

func (s *Service) ensureDataDir() error {
	dir := filepath.Dir(s.cfg.SQLitePath)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	return nil
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:    "ok",
		MachineID: s.cfg.MachineID,
		Now:       time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Service) handleRuntimeState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	sessionCount := len(s.providerSessions)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"machineId":            s.cfg.MachineID,
		"sqlitePath":           s.cfg.SQLitePath,
		"serverBaseUrl":        s.cfg.ServerBaseURL,
		"serverWsUrl":          s.cfg.ServerWSURL,
		"providerSessionCount": sessionCount,
	})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
