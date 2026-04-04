# carryOn CLI

Terminal session manager - persistent terminals you can access from anywhere.

carryOn runs as a lightweight daemon that manages terminal sessions. Sessions persist across disconnects, and you can access them from the CLI, a web browser, or IDE plugins.

## Install

### Quick install (recommended)

```bash
# macOS / Linux
curl -fsSL https://carryon.dev/get | sh

# Windows (PowerShell)
irm https://carryon.dev/get/ps1 | iex
```

### Homebrew (macOS / Linux)

```bash
brew install carryon-dev/tap/carryon
```

### Scoop (Windows)

```powershell
scoop bucket add carryon https://github.com/carryon-dev/scoop-bucket
scoop install carryon
```

### From source

```bash
go install github.com/carryon-dev/cli@latest
```

### From binary

Download the latest release from [GitHub Releases](https://github.com/carryon-dev/cli/releases).

## Quick start

```bash
# Create a session and attach
carryon --name dev

# Detach with Ctrl+C Ctrl+C (double)

# List sessions
carryon list

# Re-attach
carryon attach <session-id>

# Kill a session
carryon kill <session-id>
```

The daemon starts automatically on first use - no setup needed.

## Web UI

Access your terminals from a browser:

```bash
carryon config set local.enabled true
# Open http://127.0.0.1:8384
```

## Features

- **Persistent sessions** - terminals survive disconnects, reboots, SSH drops
- **Web access** - built-in xterm.js terminal in the browser with sidebar, tabs, mobile support
- **Pluggable backends** - native PTY (default) or tmux
- **Cross-platform** - macOS, Linux, Windows (ConPTY)
- **Project config** - define terminals per-project in `.carryon.json`
- **Auto-updates** - checks GitHub releases, applies with `carryon update`
- **Client tracking** - see who's connected (CLI, VS Code, web browser) with metadata
- **Auto-reconnect** - web UI reconnects automatically on network interruption or phone wake
- **Zero dependencies** - single static binary, no runtime needed

## Commands

```
carryon                      Create session and attach (default)
carryon list                 List all sessions
carryon attach <session>     Attach to a session
carryon kill <session>       Kill a session
carryon rename <session>     Rename a session
carryon start                Start the daemon
carryon stop                 Stop the daemon
carryon status               Show unified status
carryon config get|set|reload|path
carryon remote login|logout|status|devices
carryon logs [-f] [--level]
carryon project init|terminals|associate|disassociate
carryon update [--check]
```

## Project config

Create `.carryon.json` in your project root:

```json
{
  "version": 1,
  "terminals": [
    { "name": "server", "command": "npm run dev", "color": "green" },
    [
      { "name": "tests", "command": "npm test -- --watch", "color": "yellow" },
      { "name": "logs", "command": "tail -f app.log", "color": "cyan" }
    ]
  ]
}
```

carryOn auto-creates these terminals when you run `carryon project terminals`.

## Configuration

```bash
carryon config set default.backend native    # native or tmux
carryon config set local.port 8384           # web UI port
carryon config set local.expose true         # expose beyond localhost
carryon config set logs.level info           # debug, info, warn, error
```

Config stored at `~/.carryon/config.json`.

## Architecture

carryOn runs as a background daemon (`~/.carryon/daemon.sock`). CLI commands communicate with it over IPC using a binary framing protocol with JSON-RPC 2.0. The web UI connects via WebSocket.

Each native session runs in a **holder process** - a lightweight process that owns the PTY and survives daemon restarts. The daemon connects to holders over Unix sockets (or named pipes on Windows) and relays I/O to clients.

```
CLI ──── IPC (Unix socket / named pipe) ──── Daemon
Browser ── HTTP/WebSocket ──────────────────┘
                                              ├── Holder processes (native backend)
                                              │     └── PTY fd ── Shell process
                                              └── tmux backend (optional)
```

Sessions persist across daemon restarts:
- **Native backend**: holder processes keep PTYs alive with scrollback. Daemon reconnects on start.
- **Tmux backend**: tmux server manages persistence independently.

`carryon stop` stops the daemon but leaves sessions alive. `carryon start` reconnects to them. Use `carryon kill <session>` to explicitly end a session.

## Development

```bash
go test ./...              # run all tests
go build -o carryon .      # build binary
./carryon --version        # verify
```

## License

[FSL-1.1-ALv2](LICENSE.md)
