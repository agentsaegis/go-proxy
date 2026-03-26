# CLAUDE.md - AgentsAegis Go Proxy

## Project Overview

AgentsAegis is an open-source security awareness proxy for AI coding tools (Claude Code CLI and Claude Desktop). It sits between AI tools and the Anthropic API on `localhost:7331`, intercepting API traffic. It occasionally replaces legitimate bash commands in AI responses with realistic but inherently harmless "trap" commands (targeting nonexistent paths, fake remotes, reserved addresses). If a developer approves a trap without noticing, execution is blocked and a training message is displayed. For Claude Code CLI, blocking uses `PreToolUse` hooks. For Claude Desktop, blocking uses an MCP server that checks commands against the proxy before executing them. Results are optionally reported to the AgentsAegis dashboard API for team-level tracking and analytics.

## Tech Stack

- **Language:** Go 1.26+
- **CLI framework:** cobra (`github.com/spf13/cobra`)
- **Config:** viper (`github.com/spf13/viper`) - reads YAML config + env vars
- **YAML parsing:** `gopkg.in/yaml.v3` - for trap template files
- **Linting:** golangci-lint (errcheck, gosimple, govet, ineffassign, staticcheck, unused)
- **CI:** GitHub Actions (lint, unit tests, e2e tests, codecov)
- **Release:** GoReleaser v2 - builds darwin/linux amd64/arm64, publishes to GitHub Releases + Homebrew tap
- **Coverage:** Codecov with 90% target
- **No database** - stateless proxy; state is ephemeral (in-memory active trap + JSON trap files in `~/.agentsaegis/traps/`)

## Architecture

```
Claude Code CLI  -->  AgentsAegis Proxy (localhost:7331)  -->  Anthropic API (api.anthropic.com)
                               |
                               +--> AgentsAegis Dashboard API (optional, for config + reporting)

Claude Desktop  --[ANTHROPIC_BASE_URL]--> AgentsAegis Proxy --> Anthropic API
       |                                        |
       +--[MCP stdio]--> agentsaegis mcp -------+
                          (checks commands via     (POST /hooks/pre-tool-use)
                           proxy hook endpoint)

Copilot CLI  --[HTTPS_PROXY]--> AgentsAegis Proxy (CONNECT tunnel) --> api.github.com
                                          |
                                 TLS MITM via CAManager
                                 OAIStreamInterceptor (OpenAI SSE format)
```

**Data flow for a normal request:**
1. Claude Code sends API request to `localhost:7331` (via `ANTHROPIC_BASE_URL` env var or shell wrapper)
2. `ProxyHandler.HandleProxy()` reads the request body, checks for `tool_result` blocks that resolve active traps
3. Forwards request to Anthropic API unchanged
4. For SSE responses: `StreamInterceptor` parses events, buffers bash `tool_use` content blocks, decides whether to inject a trap
5. For JSON responses: `maybeInjectTrapInJSON()` scans `content` array for bash tool_use blocks
6. If trap injected: `CallbackHandler.RegisterTrap()` stores active trap in engine + writes trap file to disk
7. Response (possibly modified) streams back to Claude Code

**Trap resolution flow:**
1. Hook path: Claude Code's `PreToolUse` hook POSTs to `POST /hooks/pre-tool-use` - `HookHandler` matches command against active trap, blocks if matched
2. Request-body path: Next API request's `tool_result` block is checked for the trap's `tool_use_id` - detects approval/rejection
3. `CallbackHandler.ResolveTrap()` reports result to dashboard API, displays training message if missed, cleans up

**Four HTTP endpoints:**
- `GET /__aegis/health` - health check (used by shell wrapper to detect if proxy is running)
- `POST /hooks/pre-tool-use` - Claude Code PreToolUse hook endpoint
- `POST /hooks/inject-trap` - Copilot hook injection endpoint
- `/ (catch-all)` - reverse proxy to Anthropic API; also handles CONNECT for TLS MITM tunnels

## Directory Map

```
cmd/
  agentsaegis/
    main.go              # Entry point, root cobra command, version var
    cmd_start.go         # `start` command - starts proxy (foreground or --daemon)
    cmd_stop.go          # `stop` command - sends SIGTERM to daemon PID
    cmd_init.go          # `init` command - interactive setup (dashboard URL + API token)
    cmd_status.go        # `status` command - shows proxy running state, port, org connection
    cmd_report.go        # `report` command - fetches personal trap stats from dashboard
    cmd_setup_shell.go   # `setup-shell` / `remove-shell` - manages shell wrapper in .zshrc/.bashrc/.config/fish
    cmd_setup_desktop.go # `setup-desktop` / `remove-desktop` - patches Claude Desktop MCP config
    cmd_mcp.go           # `mcp` command - runs as MCP server (stdio transport) for Claude Desktop
    cmd_launch.go        # `launch claude-desktop` - launches Desktop app with proxy env var
    cmd_hook.go          # `hook` command - bridge for Copilot/VS Code PreToolUse hooks (stdin/stdout)
    cmd_setup_copilot.go # `setup-copilot` / `remove-copilot` - configures Copilot hooks + MCP in VS Code
    cmd_setup_vscode.go  # `setup-vscode` / `remove-vscode` - configures VS Code http.proxy for Copilot interception
    cmd_trust_cert.go    # `trust-cert` / `untrust-cert` - adds/removes proxy CA from system trust store
    cmd_reload.go        # `reload` command - sends SIGHUP to daemon for hot config reload
    cmd_service.go       # `install-service` / `uninstall-service` - launchd (macOS) / systemd (Linux) service management
    cmd_test.go          # Tests for all CLI commands
    cmd_init_test.go     # Tests for init command validation
    cmd_status_test.go   # Tests for status command
    cmd_setup_desktop_test.go # Tests for setup-desktop/remove-desktop

internal/
  config/
    config.go            # Config struct, Load() from ~/.agentsaegis/config.yaml + AEGIS_ env vars
    config_test.go       # Config loading tests

  server/
    server.go            # HTTP server setup, route registration, Start/Shutdown
    handler.go           # ProxyHandler - main proxy logic, SSE/JSON interception, trap injection
    stream.go            # StreamInterceptor - SSE event parsing, bash block buffering, delta rebuilding
    stream_oai.go        # OAIStreamInterceptor - OpenAI-format SSE parser for Copilot CLI CONNECT tunnels
    hook.go              # HookHandler - PreToolUse hook processing, command matching, cooldown
    tls.go               # CAManager - root CA generation/loading, per-host cert generation for TLS MITM
    connect.go           # ConnectHandler - HTTP CONNECT tunnel handler (TLS MITM for AI APIs, plain TCP otherwise)
    handler_test.go      # Proxy handler tests
    stream_test.go       # Stream interceptor tests
    stream_oai_test.go   # OAI stream interceptor tests
    hook_test.go         # Hook handler tests
    server_test.go       # Server integration tests
    tls_test.go          # CA manager and cert generation tests
    connect_test.go      # CONNECT tunnel handler tests

  trap/
    engine.go            # Engine - trap injection decision logic (frequency, jitter, cooldown, active trap)
    selector.go          # Selector - picks trap template based on command keywords, avoids repeats
    callback.go          # CallbackHandler - trap registration, resolution, dashboard reporting
    templates.go         # Template struct, LoadTemplates() from embedded YAML, ValidateTrapSafety()
    trapfile.go          # Trap file I/O (JSON files in ~/.agentsaegis/traps/ for fallback script)
    matcher.go           # MatchCommand() - structural command matching (normalization, hash, fuzzy)
    display.go           # DisplayTrainingMessage() - ANSI-colored terminal training output
    engine_test.go       # Engine tests
    selector_test.go     # Selector tests
    callback_test.go     # Callback handler tests
    templates_test.go    # Template loading + safety validation tests
    trapfile_test.go     # Trap file I/O tests
    matcher_test.go      # Command matching tests
    display_test.go      # Display tests

    traps/               # Embedded YAML trap templates (go:embed all:traps)
      destructive/       # rm -rf, git force push, docker volume nuke, db reset
      exfiltration/      # env curl, npm postinstall, netcat
      supply-chain/      # typosquat npm/pip, GitHub install
      secret-exposure/   # env console.log, git add secrets
      privilege-escalation/ # chmod 777, docker privileged
      infrastructure/    # aws s3 nuke
      windows-destructive/ # PowerShell Remove-Item, firewall disable, service stop, GPO delete, Defender disable
      windows-exfiltration/ # AD credential export, IEX cradles, NTDS.dit extraction, LSASS/SAM dump

  mcp/
    server.go            # MCP JSON-RPC 2.0 server over stdio (initialize, tools/list, tools/call)
    tools.go             # Bash tool definition, command execution, trap file fallback, training message
    hook_client.go       # HTTP client for proxy's /hooks/pre-tool-use endpoint
    server_test.go       # MCP server protocol tests
    hook_client_test.go  # Hook client tests

  daemon/
    daemon.go            # PID file management (read/write/remove), IsRunning check
    daemon_test.go       # Daemon PID tests

  client/
    client.go            # Dashboard API client - ReportEvent, FetchConfig, FetchPersonalStats, ValidateToken
    client_test.go       # Client tests

e2e/
  e2e_test.go            # End-to-end tests (build tag: e2e) - mock Anthropic + dashboard servers

install.sh               # Curl-pipe installer script - detects OS/arch, downloads release, verifies checksum

bin/                     # Build output directory (gitignored)

.github/
  workflows/
    ci.yml               # CI: lint + unit tests + e2e tests on push/PR to main
    release.yml          # Release: CI then GoReleaser on tag push (v*)
  dependabot.yml         # Weekly gomod + GitHub Actions dependency updates

docs/                    # Documentation (untracked, not in git yet)
```

## Key Flows

### 1. Trap injection via SSE stream (most common path)

1. Claude Code POST to `/ (any path)` - hits `ProxyHandler.HandleProxy()` in `internal/server/handler.go`
2. Request body checked for trap results via `checkForTrapResult()` (handler.go:376)
3. Forwarded to Anthropic API via `buildUpstreamRequestFromBody()` (handler.go:101)
4. SSE response handled by `handleSSEResponse()` (handler.go:132)
5. Each SSE event goes through `StreamInterceptor.ProcessEvent()` (stream.go:65)
6. `content_block_start` with type=tool_use, name=bash triggers buffering (stream.go:79)
7. `content_block_delta` events accumulate partial JSON (stream.go:114)
8. `content_block_stop` triggers injection decision (stream.go:141):
   - `Engine.ShouldInject()` (engine.go:67) checks frequency/jitter/cooldown
   - `Selector.SelectTrap()` (selector.go:26) picks template by keyword match
   - `buildTrapResponse()` (stream.go:195) calls `injectTrapFn` which calls `CallbackHandler.RegisterTrap()` (callback.go:40)
   - `RegisterTrap` writes trap file via `WriteTrapFile()` (trapfile.go:35)
   - Modified SSE deltas emitted via `buildModifiedDeltas()` (stream.go:251)

### 2. Trap detection via PreToolUse hook

1. Claude Code calls `POST /hooks/pre-tool-use` - hits `HookHandler.HandlePreToolUse()` (hook.go:71)
2. Validates optional `X-Hook-Secret` header
3. Parses `HookRequest` JSON: session_id, tool_name, tool_input.command
4. Only processes `PreToolUse` + `Bash` tool
5. Checks cooldown, then gets active trap from engine
6. `MatchCommand()` (matcher.go:18) compares hook command to trap command
7. If matched: `CallbackHandler.ResolveTrap()` (callback.go:83) reports "missed", activates cooldown, responds with deny
8. If not matched: responds with allow (empty 200)

### 3. Trap detection via request body (fallback)

1. Next Claude Code API request hits `HandleProxy()` (handler.go:56)
2. `checkForTrapResult()` (handler.go:376) scans messages for `tool_result` matching active trap's `tool_use_id`
3. If `is_error: true` or content contains rejection phrases - result = "caught"
4. Otherwise result = "missed"
5. Calls `CallbackHandler.ResolveTrap()`

### 4. Shell wrapper setup (Claude Code CLI)

1. `agentsaegis setup-shell` runs `runSetupShell()` (cmd_setup_shell.go:129)
2. `shellProfiles()` detects shell from `$SHELL` env var
3. Generates `_aegis_ensure_proxy()` helper and `claude()` wrapper function that:
   - Auto-starts proxy daemon if not running (waits up to 3s for health)
   - If proxy running: sets `ANTHROPIC_BASE_URL=http://localhost:PORT` then runs `command claude`
   - If proxy can't start: runs `command claude` directly (transparent fallback)
   - After Claude exits with non-zero: checks if proxy died and restarts it
4. Also generates `copilot()` wrapper with same auto-start + hook injection
5. Removes any existing marker block or legacy export, appends new wrapper

### 5. Trap detection via MCP server (Claude Desktop)

1. Claude Desktop spawns `agentsaegis mcp` as MCP server child process (stdio transport)
2. MCP server provides single `bash` tool via `tools/list`
3. LLM generates `tool_use` with `name: "bash"` - proxy injects trap in API response (same as flow 1)
4. Claude Desktop shows confirmation dialog with trap command - user approves or denies
5. If approved: Claude Desktop sends `tools/call` to MCP server via stdin
6. MCP server POSTs to `http://localhost:PORT/hooks/pre-tool-use` (same hook endpoint as flow 2)
7. If hook returns deny: MCP server returns plain text training message with `isError: true`
8. If hook returns allow: MCP server executes command via `os/exec`, returns output
9. If proxy unreachable: MCP server checks `~/.agentsaegis/traps/*.json` as fallback, then executes if no match (fail-open)

### 6. Desktop launch wrapper

1. `agentsaegis launch claude-desktop` checks proxy health
2. If proxy not running: starts it as daemon (`agentsaegis start --daemon`)
3. Launches `/Applications/Claude.app/Contents/MacOS/Claude` with `ANTHROPIC_BASE_URL=http://localhost:PORT`
4. Exits immediately (Claude Desktop runs independently)

### 7. Trap detection via Copilot hooks (GitHub Copilot)

1. Copilot agent mode fires PreToolUse hook before executing a bash command
2. Hook spawns `agentsaegis hook` which reads hook input JSON from stdin
3. `agentsaegis hook` extracts `toolName` and `toolArgs.command`
4. POSTs to `http://localhost:PORT/hooks/pre-tool-use` (same endpoint as Claude Code)
5. If proxy returns deny: writes `{"permissionDecision":"deny","permissionDecisionReason":"..."}` to stdout
6. If proxy returns allow (or is unreachable): no output (fail-open, Copilot allows execution)
7. Copilot also uses `agentsaegis mcp` as MCP server for bash tool execution (same as Claude Desktop flow 5)

## Data Models

No database. All state is ephemeral or file-based:

**ActiveTrap** (in-memory, `internal/trap/engine.go:32`):
- `ID` (string) - unique trap ID like `trap_1234567890`
- `ToolUseID` (string) - Claude's tool_use block ID for matching tool_result
- `TemplateID`, `Category`, `Severity` - from template
- `TrapCommand` (string) - the injected command
- `OriginalCommand` (string) - the replaced command
- `InjectedAt` (time.Time)
- `Triggered` (atomic.Bool), `Resolved` (atomic.Bool) - prevent double-resolution

**Template** (loaded from embedded YAML, `internal/trap/templates.go:22`):
- `id`, `category`, `subcategory`, `severity`, `name`, `description`
- `triggers.keywords[]` - command keywords that make this template relevant
- `trap_commands[]` - list of possible trap commands (one chosen at random)
- `training` - title, risk, real_world, lesson, red_flags[], time_to_read

**Trap file** (JSON on disk at `~/.agentsaegis/traps/<id>.json`, `internal/trap/trapfile.go:12`):
- `id`, `trap_command`, `template_id`, `category`, `severity`, `injected_at`, `expires_at`
- TTL: 2 minutes

**Config** (`~/.agentsaegis/config.yaml`, `internal/config/config.go:13`):
- `dashboard_url`, `api_token`, `proxy_port`, `anthropic_base_url`, `developer_id`, `org_id`, `log_level`

## Auth & Sessions

No user authentication on the proxy itself. The proxy is localhost-only.

**Dashboard API auth:** Bearer token in `Authorization` header, stored in `~/.agentsaegis/config.yaml` as `api_token`. Validated via `GET /api/proxy/config` on startup.

**Hook secret:** Optional `X-Hook-Secret` header for the `POST /hooks/pre-tool-use` endpoint. Passed as a parameter to `server.New()`. Currently not configured via config file (only test usage). Without it, any local process can call the hook endpoint (warning logged at startup).

## Environment Variables

All env vars are prefixed with `AEGIS_` and override config file values (via viper):

| Variable | Description | Required | Default |
|---|---|---|---|
| `AEGIS_DASHBOARD_URL` | Dashboard API base URL | No | `https://api.agentsaegis.com` |
| `AEGIS_API_TOKEN` | Dashboard API bearer token | No (offline mode without it) | none |
| `AEGIS_PROXY_PORT` | Port the proxy listens on | No | `7331` |
| `AEGIS_ANTHROPIC_BASE_URL` | Upstream Anthropic API URL | No | `https://api.anthropic.com` |
| `AEGIS_DEVELOPER_ID` | Developer identifier | No | none |
| `AEGIS_ORG_ID` | Organization identifier | No | none |
| `AEGIS_LOG_LEVEL` | Log level (debug/info/warn/error) | No | `info` |

## Commands

### Install dependencies
```bash
go mod download
```

### Build
```bash
make build
# Output: bin/agentsaegis
```

### Run dev server (foreground)
```bash
go run ./cmd/agentsaegis start --debug
```

### Run with super-debug mode (trap on every command)
```bash
go run ./cmd/agentsaegis start --super-debug
```

### Run as MCP server (for Claude Desktop)
```bash
go run ./cmd/agentsaegis mcp
# Reads JSON-RPC from stdin, writes to stdout, logs to stderr
```

### Setup Claude Desktop integration
```bash
agentsaegis setup-desktop    # Add MCP server to Claude Desktop config
agentsaegis remove-desktop   # Remove MCP server from Claude Desktop config
```

### Setup Copilot integration (VS Code agent mode)
```bash
agentsaegis setup-copilot    # Add hooks + MCP server to VS Code/Copilot
agentsaegis remove-copilot   # Remove hooks + MCP server from VS Code/Copilot
```

### Setup VS Code proxy for Copilot interception
```bash
agentsaegis setup-vscode     # Configure VS Code http.proxy + generate CA cert
agentsaegis remove-vscode    # Remove proxy settings from VS Code
```

### Trust / untrust proxy CA for Copilot HTTPS interception
```bash
sudo agentsaegis trust-cert    # Add proxy CA to system trust store (macOS/Linux)
sudo agentsaegis untrust-cert  # Remove proxy CA from system trust store
```
The CA is generated automatically on first proxy start at `~/.agentsaegis/ca.pem`
(key at `~/.agentsaegis/ca-key.pem`, permissions 0600).
Copilot CLI uses `HTTPS_PROXY=http://localhost:PORT` (set by the shell wrapper) which
requires the CA to be trusted for TLS MITM to work.

### Launch Claude Desktop with proxy
```bash
agentsaegis launch claude-desktop
# Starts proxy if needed, launches Desktop with ANTHROPIC_BASE_URL set
```

### Reload proxy config without restart
```bash
agentsaegis reload
# Sends SIGHUP to daemon - reloads config.yaml, dashboard settings, log level
```

### Install as system service (auto-start on login, restart on crash)
```bash
agentsaegis install-service    # macOS: launchd plist, Linux: systemd user unit
agentsaegis uninstall-service  # Remove the service
```

### Run QA tests in Docker (isolated)
```bash
make qa-docker          # Run all CLI tests (Claude + Copilot)
make qa-docker-claude   # Run Claude Code tests only
make qa-docker-copilot  # Run Copilot tests only
```
Requires Docker. Auth requirements:
- Claude: `ANTHROPIC_API_KEY` env var must be set
- Copilot: `~/.copilot/` and/or `~/.config/gh/` must exist with valid auth, or `GITHUB_TOKEN` set

### Run unit tests
```bash
make test
# Equivalent: go test -race -coverprofile=coverage.out ./...
```

### Run e2e tests
```bash
make test-e2e
# Equivalent: go test -race -tags e2e -v -count=1 ./e2e/...
```

### Lint
```bash
make lint
# Equivalent: go vet ./...
# CI also runs golangci-lint
```

### Build for production (cross-platform)
```bash
goreleaser release --snapshot --clean
```

### Deploy
Push a tag matching `v*` to trigger GitHub Actions release workflow:
```bash
git tag v0.x.x
git push origin v0.x.x
```
GoReleaser builds binaries for darwin/linux amd64/arm64, creates GitHub Release, and publishes Homebrew formula.

## Common Tasks

### Add a new trap template
1. Create a YAML file in `internal/trap/traps/<category>/<name>.yml`
2. Follow the structure of existing templates (see `rm-rf-expand.yml` for reference)
3. Required fields: `id`, `category`, `severity`, `trap_commands`, `triggers.keywords`, `training.title`
4. Trap commands MUST be inherently harmless - target `/tmp/.aegis-trap*`, `0.0.0.0`, `aegis-trap-nonexistent*`, `--dry-run`, or similar safe patterns
5. Run `make test` - `ValidateTrapSafety()` will reject unsafe commands
6. Templates are embedded at compile time via `go:embed all:traps`

### Add a new CLI command
1. Create `cmd/agentsaegis/cmd_<name>.go`
2. Define a `cobra.Command` var and register it in `init()` via `rootCmd.AddCommand()`
3. Add tests in `cmd/agentsaegis/cmd_test.go` or a new `cmd_<name>_test.go`

### Add a new dashboard API endpoint
1. Add method to `internal/client/client.go` following the pattern of `ReportEvent()` or `FetchConfig()`
2. All requests use `Authorization: Bearer <token>` header
3. Timeout is 10 seconds

### Add a new safety check for trap commands
1. Add an entry to `unsafeChecks` slice in `internal/trap/templates.go:138`
2. Each check has a name (string) and a `func(cmd string) bool` that returns true if the command is unsafe
3. Add test cases in `templates_test.go`

### Modify trap injection logic
1. Injection decision: `internal/trap/engine.go` - `ShouldInject()`
2. Template selection: `internal/trap/selector.go` - `SelectTrap()`
3. SSE injection: `internal/server/stream.go` - `handleBlockStop()` and `buildTrapResponse()`
4. JSON injection: `internal/server/handler.go` - `maybeInjectTrapInJSON()`

## Testing

- **Unit tests:** Every `*.go` file has a corresponding `*_test.go` in the same package
- **E2E tests:** `e2e/e2e_test.go` with build tag `e2e` (not included in `make test`)
- **Framework:** Standard `testing` package only, no external test framework
- **Coverage target:** 90% (enforced by Codecov)
- **Test pattern:** Table-driven tests, `t.TempDir()` for filesystem tests, `httptest.NewServer` for HTTP mocking, `t.Setenv()` for env vars
- **Run all:** `make test && make test-e2e`

To write a new test:
1. Create a test function `TestXxx` in the corresponding `_test.go` file
2. For tests that need config: use the `setupTestHome(t)` helper in cmd tests, or create temp dirs with `t.TempDir()`
3. For tests that need an HTTP server: use `httptest.NewServer`
4. For e2e tests: add to `e2e/e2e_test.go`, mock both Anthropic and dashboard servers

## Gotchas

- **Fail-open by design.** This is the most important architectural decision in the proxy. If the proxy is down, Claude Code hooks fail open - commands execute without any trap checking. The shell wrapper (`claude()` function) auto-starts the proxy if it's not running, and restarts it after a non-zero exit. If the proxy is unreachable even after auto-start, Claude Code talks directly to Anthropic with no interception at all. The trap file mechanism (`~/.agentsaegis/traps/*.json`) exists as a fallback for detecting active traps even if the hook HTTP call fails. Any future developer must preserve this fail-open guarantee - the proxy must never prevent Claude Code from working.

- **Trap commands must be inherently safe.** `ValidateTrapSafety()` rejects commands that could cause real harm. All `rm` targets must be under `/tmp/.aegis-trap*`, all network destinations must be `0.0.0.0` (connection refused), all packages must use `aegis-trap-nonexistent` prefix, all git operations must use `--dry-run` or `aegis-nonexistent-remote`. If you add a trap that fails safety validation, it's silently dropped at startup.

- **Accept-Encoding is stripped from upstream requests** (handler.go:128). Without this, Anthropic sends gzip-compressed SSE streams that the proxy can't parse for trap injection.

- **Only one active trap at a time.** `Engine.ShouldInject()` returns false while there's an active trap or pending injection. The `pendingInject` flag prevents TOCTOU races between `ShouldInject()` and `SetActiveTrap()`.

- **Trap resolution is idempotent.** `ActiveTrap.Resolved` is an `atomic.Bool` - the first call to `ResolveTrap()` wins, subsequent calls are no-ops. Both the hook path and request-body path can detect resolution, so this prevents double-reporting.

- **Hook cooldown.** After a trap is resolved via the hook, `HookHandler` suppresses the next 10 commands (`hookCooldownCommands`) to avoid re-blocking related commands in the same sequence.

- **SSE panic recovery.** The SSE interceptor runs inside `processSSEStream()` with `defer recover()`. If the interceptor panics, remaining upstream data is passed through unmodified via `drainSSEPassthrough()`. Similarly, `safeInjectTrapInJSON()` wraps JSON injection with panic recovery. The proxy never crashes from a bad trap injection.

- **Upstream timeout is 120s for initial response** (`ResponseHeaderTimeout` on the transport). This returns 504 if Anthropic doesn't start responding within 120 seconds, but does NOT cut off long-running SSE streams once headers are received.

- **SSE buffering.** The `StreamInterceptor` buffers ALL events for a bash tool_use content block until `content_block_stop`. Non-bash blocks pass through immediately. If injection fails, buffered events are flushed unchanged.

- **Scanner buffer size.** SSE scanner uses a 1MB max buffer (`handler.go:159`) for large payloads. If an SSE event exceeds this, the scanner will error.

- **Config file location is hardcoded** to `~/.agentsaegis/config.yaml`. The `AEGIS_` env prefix overrides config values but there's no CLI flag to specify a different config path.

- **SIGHUP reloads config without restart.** `agentsaegis reload` sends SIGHUP to the daemon. The proxy re-reads config.yaml, fetches dashboard config, and updates trap frequency, categories, difficulty, and log level. Port and upstream URL require a full restart.

- **Remote config polling every 5 minutes.** When an API token is configured, the proxy polls `GET /api/proxy/config` on the dashboard. If the dashboard is unreachable, current config is kept. Disabled in super-debug mode.

- **Heartbeat file at `~/.agentsaegis/heartbeat`** is written every 30 seconds with a Unix timestamp. Cleaned up on shutdown. Can be used by external tools to detect if the proxy is alive without an HTTP request.

- **Hook health monitor warns but never denies.** The `agentsaegis hook` command checks proxy health before calling hook endpoints. If the proxy is down, it warns to stderr (up to 5 times) and allows the command. State is tracked in `~/.agentsaegis/hook_health_failures`.

- **`install-service` writes the absolute binary path** to the plist/unit file. After a Homebrew upgrade that changes the binary path, re-run `install-service`.

- **The shell wrapper uses a `claude()` function**, not an alias or env export. This means the proxy is only used when running `claude` - other tools using the Anthropic API won't be proxied unless they also use `ANTHROPIC_BASE_URL`.

- **The `copilot()` shell wrapper sets `HTTPS_PROXY`** rather than injecting hook files. Copilot CLI routes all HTTPS traffic through the proxy as a CONNECT tunnel. The proxy performs TLS MITM for `api.github.com` using the CA from `~/.agentsaegis/ca.pem`. The CA must be trusted by the system (via `sudo agentsaegis trust-cert`) for TLS MITM to work without TLS errors.

- **CONNECT MITM only targets known AI API hosts** (`api.github.com`, `*.githubcopilot.com`, `copilot-proxy.githubusercontent.com`, `copilot-telemetry.githubusercontent.com`). All other CONNECT targets are passed through as plain TCP tunnels without TLS termination. To add more hosts, update `mitmHosts` or `mitmSuffixes` in `internal/server/connect.go`.

- **The TLS proxy CA key** is stored at `~/.agentsaegis/ca-key.pem` with 0600 permissions. The CA cert is at `~/.agentsaegis/ca.pem` (0644). Both are generated on first proxy start if absent. After a Homebrew upgrade, the CA files persist; only re-run `sudo agentsaegis trust-cert` if the key was rotated.

- **`--super-debug` mode** injects a trap on every single bash command and auto-clears stale traps. It also disables cooldown and jitter on the hook handler. Use it for testing trap injection/detection mechanics.

- **Trap templates are embedded at compile time** via `go:embed all:traps` in `templates.go`. Changes to YAML files require recompilation. There's no runtime template loading.

- **The `expired` result is mapped to `missed`** when reporting to the dashboard API (callback.go:123) because the DB constraint only allows missed/caught/edited.

- **MCP server is a separate process** spawned by Claude Desktop as a child process. It communicates via stdio (JSON-RPC 2.0) and checks commands by POSTing to the proxy's hook endpoint over HTTP. If the proxy is down, it falls back to checking trap files on disk, then executes the command (fail-open).

- **Claude Desktop launch wrapper** requires the Desktop app to respect `ANTHROPIC_BASE_URL` env var. If the Electron app doesn't read this env var, API traffic won't be proxied and trap injection won't work (the MCP server will still block traps via hook/trap-file fallback).

- **`agentsaegis setup-desktop` writes the absolute binary path** to Claude Desktop's config. After a Homebrew upgrade that changes the binary path, re-run `setup-desktop`.

- **Docker QA uses `--super-debug` mode** for guaranteed trap injection on every command. Container entrypoint starts proxy as `qa` user (non-root) via `gosu`, then drops privileges for test scripts.

- **Container CA trust runs as root** in the entrypoint (before dropping to `qa`). The proxy CA is generated at proxy start, then `agentsaegis trust-cert` installs it to `/usr/local/share/ca-certificates/` and `update-ca-certificates` rebuilds the system bundle. If TLS MITM tests fail, this step likely failed.

- **Host auth is mounted read-only** (`~/.copilot/:ro`, `~/.config/gh/:ro`). Container never writes to host credentials. If the directories don't exist, the mount is skipped and Copilot tests are skipped (not failed).

- **Copilot CLI npm package name may differ** from the Homebrew formula. `Dockerfile.qa` tries `@anthropic-ai/copilot-cli` then `@github-copilot/cli`. If neither installs, copilot QA tests are skipped automatically.

- **`scripts/qa-docker.sh` must be run from repo root** (or via `make qa-docker`). It sets the Docker build context to the repo root.

## External Services

### AgentsAegis Dashboard API (`api.agentsaegis.com`)
- **Purpose:** Org config (trap frequency, categories, difficulty), trap event reporting, personal stats
- **If unavailable:** Proxy runs in offline mode with default config (`TrapFrequency: 50`, `MaxTrapsPerDay: 2`, all categories). Events are not reported. Warning logged at startup.
- **API key:** Configured via `api_token` in config or `AEGIS_API_TOKEN` env var
- **Endpoints used:**
  - `GET /api/proxy/config` - fetch org config + validate token
  - `POST /api/proxy/events` - report trap results
  - `GET /api/dashboard/team/me` - personal stats

### Anthropic API (`api.anthropic.com`)
- **Purpose:** Upstream API that Claude Code talks to
- **If unavailable:** Proxy returns 502 Bad Gateway
- **API key:** Passed through from Claude Code (proxy does not manage Anthropic keys)

## Related Repos

- **agentsaegis/homebrew-tap** - Homebrew formula for `agentsaegis` (auto-updated by GoReleaser)
- **AgentsAegis monorepo** (private, not public) - Go API + React web app for team management, assessments, analytics, and training. This is the backend that serves `api.agentsaegis.com` and the web dashboard at `agentsaegis.com`.
