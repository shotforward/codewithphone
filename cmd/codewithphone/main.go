package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shotforward/codewithphone/internal/app"
	"github.com/shotforward/codewithphone/internal/config"
)

const version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		cmdStart(os.Args[2:])
	case "stop":
		cmdStop()
	case "status":
		cmdStatus()
	case "mcp-stdio":
		app.RunMCPStdio()
	case "version", "--version", "-v":
		fmt.Printf("codewithphone %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `codewithphone — PocketCode local AI agent runtime

Usage:
  codewithphone start [flags]    Start the agent
  codewithphone stop             Stop a running background agent
  codewithphone status           Check if the agent is running
  codewithphone mcp-stdio        Run as MCP stdio bridge (internal)
  codewithphone version          Print version

Start flags:
  -f, --foreground           Run in foreground (default: background after binding)
  -s, --server URL           Server base URL
  -p, --port PORT            Local port (0 = auto-select, default)
  -w, --workspace DIR        Workspace root directory
  -c, --config FILE          Config file path
  -n, --max-workers N        Max concurrent task workers (1-32)
      --bind-mode MODE       Binding mode: auto|force|token_only

Environment:
  CODEWITHPHONE_HOME         Home directory (default: ~/.codewithphone)
  CODEWITHPHONE_CONFIG       Config file path
  CODEWITHPHONE_ADDR         Listen address (default: 127.0.0.1:0)
  DAEMON_SERVER_BASE_URL     Server URL (default: https://codewithphone.com/api)
  DAEMON_ALLOWED_ROOTS       Colon-separated workspace roots
`)
}

type startFlags struct {
	foreground bool
	server     string
	port       string
	workspace  string
	configFile string
	maxWorkers int
	bindMode   string
}

func parseStartFlags(args []string) (startFlags, error) {
	var f startFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-f" || arg == "--foreground":
			f.foreground = true
		case arg == "-d" || arg == "--daemon":
			// Legacy flag — now the default behavior. Accepted silently
			// for backwards compatibility but has no effect.
		case arg == "-s" || arg == "--server":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s requires a value", arg)
			}
			i++
			f.server = args[i]
		case strings.HasPrefix(arg, "--server="):
			f.server = strings.TrimPrefix(arg, "--server=")
		case arg == "-p" || arg == "--port":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s requires a value", arg)
			}
			i++
			f.port = args[i]
		case strings.HasPrefix(arg, "--port="):
			f.port = strings.TrimPrefix(arg, "--port=")
		case arg == "-w" || arg == "--workspace":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s requires a value", arg)
			}
			i++
			f.workspace = args[i]
		case strings.HasPrefix(arg, "--workspace="):
			f.workspace = strings.TrimPrefix(arg, "--workspace=")
		case arg == "-c" || arg == "--config":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s requires a value", arg)
			}
			i++
			f.configFile = args[i]
		case strings.HasPrefix(arg, "--config="):
			f.configFile = strings.TrimPrefix(arg, "--config=")
		case arg == "-n" || arg == "--max-workers":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s requires a value", arg)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return f, fmt.Errorf("invalid --max-workers value: %s", args[i])
			}
			f.maxWorkers = n
		case strings.HasPrefix(arg, "--max-workers="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--max-workers="))
			if err != nil {
				return f, fmt.Errorf("invalid --max-workers value: %s", arg)
			}
			f.maxWorkers = n
		case strings.HasPrefix(arg, "--bind-mode="):
			f.bindMode = strings.TrimPrefix(arg, "--bind-mode=")
		case arg == "--bind-mode":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--bind-mode requires a value")
			}
			i++
			f.bindMode = args[i]
		default:
			return f, fmt.Errorf("unknown flag: %s", arg)
		}
	}
	return f, nil
}

func cmdStart(args []string) {
	flags, err := parseStartFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Set env vars from flags before loading config.
	if flags.configFile != "" {
		os.Setenv("CODEWITHPHONE_CONFIG", flags.configFile)
	}
	if flags.server != "" {
		os.Setenv("DAEMON_SERVER_BASE_URL", flags.server)
	}
	if flags.port != "" {
		os.Setenv("CODEWITHPHONE_ADDR", "127.0.0.1:"+flags.port)
	}
	if flags.workspace != "" {
		os.Setenv("DAEMON_ALLOWED_ROOTS", flags.workspace)
	}

	// Check if already running.
	if pid := readPID(); pid > 0 {
		if isProcessRunning(pid) {
			fmt.Fprintf(os.Stderr, "codewithphone is already running (pid %d). Use 'codewithphone stop' first.\n", pid)
			os.Exit(1)
		}
		// Stale PID file.
		os.Remove(config.PIDPath())
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if flags.bindMode != "" {
		cfg.BindMode = flags.bindMode
	}
	cfg.BindMode, err = config.ParseBindMode(cfg.BindMode)
	if err != nil {
		log.Fatalf("invalid bind mode: %v", err)
	}
	if flags.maxWorkers > 0 {
		cfg.MaxConcurrentTurns = flags.maxWorkers
	}

	// Ensure home dir exists for PID file and local secrets.
	_ = os.MkdirAll(config.HomeDir(), 0o700)
	_ = os.Chmod(config.HomeDir(), 0o700)

	// Default: background mode. Binding runs interactively (user sees the PIN
	// and confirms in the terminal), then the agent forks to the background.
	// Use -f/--foreground to stay in the terminal (useful for debugging).
	if !flags.foreground {
		// Run binding in foreground so user can see the PIN and confirm.
		svc := app.New(cfg) // interactive=true by default
		bindCtx, bindCancel := signal.NotifyContext(
			context.Background(),
			os.Interrupt,
			syscall.SIGTERM,
			syscall.SIGHUP,
			syscall.SIGQUIT,
		)
		if err := svc.EnsureBinding(bindCtx); err != nil {
			bindCancel()
			log.Fatalf("binding failed: %v", err)
		}
		bindCancel()

		// Binding succeeded — token is saved in config.
		// Now daemonize with token_only so the child skips binding.
		daemonize(args)
		return
	}

	// Foreground mode (-f/--foreground).
	// Write PID file.
	if err := writePID(os.Getpid()); err != nil {
		log.Printf("warning: failed to write PID file: %v", err)
	}
	defer os.Remove(config.PIDPath())

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	)
	defer cancel()

	svc := app.New(cfg)
	if err := svc.Run(ctx); err != nil {
		log.Fatalf("codewithphone exited with error: %v", err)
	}
}

func daemonize(originalArgs []string) {
	// Build args for the child process: add --foreground (the child runs in
	// foreground inside the detached process) and strip any --bind-mode flag.
	childArgs := []string{"start", "--foreground"}
	skipNext := false
	for _, a := range originalArgs {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-d" || a == "--daemon" || a == "-f" || a == "--foreground" {
			continue
		}
		if a == "--bind-mode" {
			skipNext = true // skip the next arg (the value)
			continue
		}
		if strings.HasPrefix(a, "--bind-mode=") {
			continue
		}
		childArgs = append(childArgs, a)
	}
	// Force token_only so the background child skips binding (already done in foreground).
	childArgs = append(childArgs, "--bind-mode=token_only")

	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("cannot determine executable path: %v", err)
	}

	_ = os.MkdirAll(config.HomeDir(), 0o700)
	_ = os.Chmod(config.HomeDir(), 0o700)
	logFile, err := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Fatalf("cannot open log file: %v", err)
	}

	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		log.Fatalf("failed to start background process: %v", err)
	}

	fmt.Printf("codewithphone started in background (pid %d)\n", cmd.Process.Pid)
	fmt.Printf("  log: %s\n", config.LogPath())

	// Detach — don't wait for child.
	_ = logFile.Close()
}

func cmdStop() {
	pid := readPID()
	if pid <= 0 {
		fmt.Println("codewithphone is not running (no PID file found).")
		return
	}

	if !isProcessRunning(pid) {
		fmt.Printf("codewithphone is not running (stale PID %d). Cleaning up.\n", pid)
		os.Remove(config.PIDPath())
		return
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find process %d: %v\n", pid, err)
		os.Exit(1)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "failed to stop codewithphone (pid %d): %v\n", pid, err)
		os.Exit(1)
	}

	// Wait up to 5 seconds for graceful shutdown.
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if !isProcessRunning(pid) {
			fmt.Printf("codewithphone stopped (pid %d).\n", pid)
			os.Remove(config.PIDPath())
			return
		}
	}
	fmt.Printf("codewithphone (pid %d) did not exit in time, sending SIGKILL.\n", pid)
	_ = process.Kill()
	os.Remove(config.PIDPath())
}

func cmdStatus() {
	pid := readPID()
	if pid <= 0 {
		fmt.Println("codewithphone is not running.")
		os.Exit(1)
	}

	if !isProcessRunning(pid) {
		fmt.Printf("codewithphone is not running (stale PID %d).\n", pid)
		os.Remove(config.PIDPath())
		os.Exit(1)
	}

	fmt.Printf("codewithphone is running (pid %d).\n", pid)

	// Try to reach the healthz endpoint via the log to find the port.
	// Read last lines of log to find the listen address.
	if addr := findListenAddr(); addr != "" {
		resp, err := http.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err == nil {
			defer resp.Body.Close()
			var health map[string]any
			if json.NewDecoder(resp.Body).Decode(&health) == nil {
				fmt.Printf("  address:    %s\n", addr)
				if mid, ok := health["machineId"]; ok && mid != "" {
					fmt.Printf("  machine-id: %s\n", mid)
				}
			}
		}
	}
}

// findListenAddr scans the log file for the last "listening on" line.
func findListenAddr() string {
	data, err := os.ReadFile(config.LogPath())
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if idx := strings.Index(lines[i], "codewithphone listening on "); idx >= 0 {
			return strings.TrimSpace(lines[i][idx+len("codewithphone listening on "):])
		}
	}
	return ""
}

func writePID(pid int) error {
	return os.WriteFile(config.PIDPath(), []byte(strconv.Itoa(pid)), 0o600)
}

func readPID() int {
	data, err := os.ReadFile(config.PIDPath())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, signal 0 checks if the process exists.
	return process.Signal(syscall.Signal(0)) == nil
}
