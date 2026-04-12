# codewithphone

Local AI agent runtime for [CodeWithPhone](https://codewithphone.com). Connects your machine to the CodeWithPhone platform so you can run AI coding tasks using Claude, Gemini, and Codex CLIs directly on your local environment.

## How it works

`codewithphone` runs on your machine and acts as a bridge between the CodeWithPhone platform and local AI CLI tools:

1. You create a coding session on [codewithphone.com](https://codewithphone.com) (or the mobile app)
2. The agent picks up tasks and executes them using installed AI runtimes (Claude Code, Gemini CLI, Codex CLI)
3. Code changes happen on your machine, in your real project directories
4. You review and approve changes through the web interface

```
CodeWithPhone App/Web  <───>  codewithphone.com  <───>  codewithphone (your machine)
                                                              │
                                                    ┌─────────┼─────────┐
                                                    ▼         ▼         ▼
                                                 Claude    Gemini    Codex
                                                  Code      CLI       CLI
```

## Quick start

### 1. Install

**One command (GitHub Release):**

```bash
curl -fsSL https://raw.githubusercontent.com/shotforward/codewithphone/main/install.sh | bash
```

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/shotforward/codewithphone/main/install.sh | bash -s -- v0.2.0
```

**From source (requires Go 1.25+):**

```bash
git clone https://github.com/shotforward/codewithphone.git
cd codewithphone
go build -o codewithphone ./cmd/codewithphone
sudo mv codewithphone /usr/local/bin/
```

### 2. Install at least one AI runtime

| Runtime | Install |
|---------|---------|
| [Claude Code](https://docs.anthropic.com/en/docs/claude-code) | `npm install -g @anthropic-ai/claude-code` |
| [Gemini CLI](https://github.com/google-gemini/gemini-cli) | `npm install -g @google/gemini-cli` |
| [Codex CLI](https://github.com/openai/codex) | `npm install -g @openai/codex` |

You only need one. `codewithphone` will auto-detect all installed runtimes.

### 3. Start the agent

```bash
codewithphone start
```

On first run, a **PIN code** will be displayed:

```
  ┌────────────────────────────────────────────────────────┐
  │  CodeWithPhone Authorization Required                  │
  │                                                        │
  │  PIN CODE:      ABC123                                 │
  │                                                        │
  │  Please enter this PIN on your CodeWithPhone Web/App.  │
  │  Waiting for your confirmation...                      │
  └────────────────────────────────────────────────────────┘
```

Open [codewithphone.com](https://codewithphone.com), log in, and enter the PIN. The agent will then ask you to confirm the binding in the terminal:

```
  Binding request from user@example.com (John)
  Approve? [Y/n]: y
  Device authorized successfully!
```

Once paired, the agent starts accepting tasks.

### 4. Run in background (recommended)

```bash
codewithphone start -d
```

When using `-d` (daemon mode), pairing still happens interactively in your terminal — you'll see the PIN and confirm the binding just like in foreground mode. After confirmation, the agent automatically moves to the background.

Check status and stop:

```bash
codewithphone status    # show PID, listen address, machine ID
codewithphone stop      # graceful shutdown
```

That's it. Open [codewithphone.com](https://codewithphone.com) and start a coding session.

## Usage

```
codewithphone start [flags]    Start the agent
codewithphone stop             Stop a running background agent
codewithphone status           Check if the agent is running
codewithphone version          Print version
```

### Start flags

| Flag | Description | Default |
|------|-------------|---------|
| `-d, --daemon` | Run in background (after pairing) | foreground |
| `-s, --server URL` | Server URL | `https://codewithphone.com` |
| `-p, --port PORT` | Local port (`0` = auto) | `0` (auto-select) |
| `-w, --workspace DIR` | Workspace root directory | `$HOME` |
| `-c, --config FILE` | Config file path | `~/.codewithphone/config.yaml` |
| `-n, --max-workers N` | Max concurrent tasks (1-32) | `10` |
| `--bind-mode MODE` | `auto`, `force`, or `token_only` | `auto` |

### Examples

```bash
# Start in foreground
codewithphone start

# Start in background (pairing is still interactive)
codewithphone start -d

# Start with a specific workspace
codewithphone start -w ~/projects

# Start in background pointing to a custom server
codewithphone start -d -s https://my-server.example.com

# Re-pair this machine (get a new PIN)
codewithphone start --bind-mode=force

# Start with a fixed port
codewithphone start -p 9090
```

## Configuration

Config is stored in `~/.codewithphone/config.yaml` and is auto-created on first pairing.

```yaml
daemon:
  http_addr: 127.0.0.1:0
  machine_id: machine-xxxx        # assigned by server
  machine_token: "..."            # assigned on pairing
  bind_mode: auto
  max_concurrent_turns: 10
  server_base_url: https://codewithphone.com/api
  codex_bin: codex
  gemini_bin: gemini
  gemini_model: gemini-3-flash-preview
  claude_bin: claude
  claude_model: sonnet
```

### File layout

```
~/.codewithphone/
├── config.yaml         # Configuration (auto-managed)
├── codewithphone.pid   # PID file (when running in background)
├── codewithphone.log   # Log file (when running in background)
└── data/
    └── agent.db        # Local state (SQLite)
```

Security defaults:
- `~/.codewithphone` is created with `0700`
- `config.yaml`, `codewithphone.log`, and `codewithphone.pid` are written with `0600`

### Environment variables

All settings can be overridden via environment variables:

| Variable | Description |
|----------|-------------|
| `CODEWITHPHONE_HOME` | Home directory (default: `~/.codewithphone`) |
| `CODEWITHPHONE_CONFIG` | Config file path |
| `CODEWITHPHONE_ADDR` | Listen address (e.g. `127.0.0.1:8081`) |
| `DAEMON_SERVER_BASE_URL` | Server API URL |
| `DAEMON_ALLOWED_ROOTS` | Colon-separated workspace root paths |
| `DAEMON_MAX_CONCURRENT_TURNS` | Max parallel task workers |
| `DAEMON_CODEX_BIN` | Path to Codex CLI binary |
| `DAEMON_GEMINI_BIN` | Path to Gemini CLI binary |
| `DAEMON_CLAUDE_BIN` | Path to Claude Code binary |

## How pairing works

1. `codewithphone start` generates a 6-character PIN and displays it in the terminal
2. You enter the PIN on [codewithphone.com](https://codewithphone.com) while logged in
3. The agent detects your entry and prompts you to confirm in the terminal (`Approve? [Y/n]`)
4. You press `Y` (or Enter) to approve, or `n` to reject and generate a new PIN
5. A machine token is issued and saved to `~/.codewithphone/config.yaml`
6. Subsequent starts reuse the token automatically (no re-pairing needed)

With `-d` (daemon mode), the pairing flow runs in the foreground first. Once approved, the agent forks to the background automatically.

To re-pair (keeps the same machine ID, only refreshes the auth token):

```bash
codewithphone start --bind-mode=force
```

## Security

- **Local confirmation required**: Every new device pairing must be approved in the terminal where `codewithphone` is running. Even if someone knows your PIN, they cannot bind to your machine without your explicit approval.
- **Token-based auth**: After pairing, communication uses a machine token stored locally. No passwords are transmitted.
- **Local listener**: The HTTP server binds to `127.0.0.1` by default (localhost only). It is not accessible from the network.

For vulnerability reporting, see [SECURITY.md](SECURITY.md).

## Building from source

```bash
git clone https://github.com/shotforward/codewithphone.git
cd codewithphone
make build          # produces bin/codewithphone
make test           # run tests
```

## Release Artifacts

Each tag release uploads:
- `codewithphone_<tag>_linux_amd64.tar.gz`
- `codewithphone_<tag>_linux_arm64.tar.gz`
- `codewithphone_<tag>_darwin_amd64.tar.gz`
- `codewithphone_<tag>_darwin_arm64.tar.gz`
- `checksums.txt`

## Project Governance

- Contribution guide: [CONTRIBUTING.md](CONTRIBUTING.md)
- Security policy: [SECURITY.md](SECURITY.md)
- Support policy: [SUPPORT.md](SUPPORT.md)
- Code of conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- Trademark policy: [TRADEMARK.md](TRADEMARK.md)

## License

Apache License 2.0
