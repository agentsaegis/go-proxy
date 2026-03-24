# CLAUDE.md - AgentsAegis Go Proxy

## Project Overview

AgentsAegis is an open-source security awareness proxy for AI coding tools (Claude Code CLI, with Copilot CLI support in development). It sits between the AI tool and its API on `localhost:7331`, intercepting API traffic. It occasionally replaces legitimate bash commands in AI responses with realistic but inherently harmless "trap" commands (targeting nonexistent paths, fake remotes, reserved addresses). If a developer approves a trap without noticing, the proxy detects this via the next API request's tool_result and reports it. Results are optionally reported to the AgentsAegis dashboard API for team-level tracking and analytics.

## Tech Stack

- **Language:** Go 1.26+
- **CLI framework:** cobra (`github.com/spf13/cobra`)
- **Config:** viper (`github.com/spf13/viper`) - reads YAML config + env vars
- **YAML parsing:** `gopkg.in/yaml.v3` - for trap template files
- **Linting:** golangci-lint v2 (errcheck, govet, ineffassign, staticcheck, unused)
- **CI:** GitHub Actions (lint, unit tests, e2e tests, codecov)
- **Release:** GoReleaser v2 - builds darwin/linux amd64/arm64, publishes to GitHub Releases + Homebrew tap
- **Coverage:** Codecov with 90% target
- **No database** - stateless proxy; state is ephemeral (in-memory active trap + JSON trap files in `~/.agentsaegis/traps/`)

## Architecture

```
Claude Code  -->  AgentsAegis Proxy (localhost:7331)  -->  Anthropic API (api.anthropic.com)
                          |
                          +--> AgentsAegis Dashboard API (optional, for config + reporting)
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
1. Request-body path (primary): Next API request's `tool_result` block is checked for the trap's `tool_use_id`. Content is scanned for rejection phrases ("was rejected", "doesn't want to proceed", "user denied") to distinguish user rejection (caught) from command failure (missed). The `is_error` flag is NOT used because trap commands always fail with `is_error: true`.
2. Hook path (secondary): Claude Code's `PreToolUse` hook POSTs to `POST /hooks/pre-tool-use` - `HookHandler` matches command against active trap, blocks if matched. Note: this fires before the user confirmation dialog, so it's less reliable.
3. `CallbackHandler.ResolveTrap()` reports result to dashboard API, displays training message if missed, cleans up

**HTTP endpoints:**
- `GET /__aegis/health` - health check (used by shell wrapper to detect if proxy is running)
- `POST /hooks/pre-tool-use` - PreToolUse hook endpoint (matches commands against active traps)
- `POST /hooks/inject-trap` - hook-based trap injection for non-proxied clients
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
    cmd_test.go          # Tests for all CLI commands
    cmd_init_test.go     # Tests for init command validation
    cmd_status_test.go   # Tests for status command

internal/
  config/
    config.go            # Config struct, Load() from ~/.agentsaegis/config.yaml + AEGIS_ env vars
    config_test.go       # Config loading tests

  server/
    server.go            # HTTP server setup, route registration, Start/Shutdown
    handler.go           # ProxyHandler - main proxy logic, SSE/JSON interception, trap injection
    stream.go            # StreamInterceptor - SSE event parsing, bash block buffering, delta rebuilding
    hook.go              # HookHandler - PreToolUse hook processing, command matching, cooldown
    handler_test.go      # Proxy handler tests
    stream_test.go       # Stream interceptor tests
    hook_test.go         # Hook handler tests
    server_test.go       # Server integration tests

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

  daemon/
    daemon.go            # PID file management (read/write/remove), IsRunning check
    daemon_test.go       # Daemon PID tests

  client/
    client.go            # Dashboard API client - ReportEvent, FetchConfig, FetchPersonalStats, ValidateToken
    client_test.go       # Client tests

e2e/
  e2e_test.go            # End-to-end tests (build tag: e2e) - mock Anthropic + dashboard servers

scripts/
  test-interactive.sh    # Launch interactive Docker container for manual testing
  qa-docker-entrypoint.sh # Container entrypoint (proxy start, CA trust, wrapper setup)
  qa-docker-claude.sh    # Automated Claude Code QA tests
  qa-docker-copilot.sh   # Automated Copilot CLI QA tests

install.sh               # Curl-pipe installer script - detects OS/arch, downloads release, verifies checksum
Dockerfile.qa            # Docker image with Claude Code + Copilot CLI for testing

bin/                     # Build output directory (gitignored)

.github/
  workflows/
    ci.yml               # CI: lint (golangci-lint v2) + unit tests + e2e tests on push/PR to main
    release.yml          # Release: CI then GoReleaser on tag push (v*)
  dependabot.yml         # Weekly gomod + GitHub Actions dependency updates
```

## Key Flows

### 1. Trap injection via SSE stream (primary path)

1. Claude Code POSTs to `/ (any path)` - `ProxyHandler.HandleProxy()` in `handler.go`
2. Request body checked for trap results via `checkForTrapResult()`
3. Forwarded to Anthropic API
4. SSE response piped through `StreamInterceptor.ProcessEvent()` in `stream.go`
5. `content_block_start` with type=tool_use, name=bash triggers buffering
6. `content_block_delta` events accumulate partial JSON
7. `content_block_stop` triggers injection decision:
   - `Engine.ShouldInject()` checks frequency/jitter/cooldown
   - `Selector.SelectTrap()` picks template by keyword match
   - `CallbackHandler.RegisterTrap()` stores trap + writes trap file to disk
   - Modified SSE deltas emitted back to Claude Code

### 2. Trap detection via request body (primary detection)

1. Next Claude Code API request hits `HandleProxy()`
2. `checkForTrapResult()` scans messages for `tool_result` matching active trap's `tool_use_id`
3. Content checked for rejection phrases ("was rejected", "doesn't want to proceed", "user denied") - result = "caught"
4. No rejection phrases found - result = "missed" (command ran but failed on nonexistent targets)
5. `CallbackHandler.ResolveTrap()` reports to dashboard

### 3. Trap detection via PreToolUse hook (secondary)

1. Claude Code calls `POST /hooks/pre-tool-use` - `HookHandler.HandlePreToolUse()` in `hook.go`
2. Validates optional `X-Hook-Secret` header, parses command from request
3. `MatchCommand()` compares against active trap
4. If matched: resolves as "missed", responds with deny
5. Note: fires before user confirmation dialog, so not reliable as primary detection

### 4. Shell wrapper setup

1. `agentsaegis setup-shell` detects shell from `$SHELL`
2. Generates `claude()` and `copilot()` wrapper functions:
   - `claude()`: sets `ANTHROPIC_BASE_URL=http://localhost:PORT`, auto-starts proxy
   - `copilot()`: sets `HTTPS_PROXY=http://localhost:PORT`, auto-starts proxy
   - Both fail open - if proxy can't start, run command directly

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

### Docker QA (interactive testing)
```bash
# Starts container with proxy in super-debug mode, CA trusted, shell wrappers configured
./scripts/test-interactive.sh

# Inside container:
claude          # Test Claude Code traps (requires browser auth)
copilot         # Test Copilot traps (use /login for device auth)

# Connect to local dashboard:
export AEGIS_API_TOKEN=aa_dev_...
export AEGIS_DASHBOARD_URL=http://host.docker.internal:3763
./scripts/test-interactive.sh
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

- **Fail-open by design.** This is the most important architectural decision in the proxy. If the proxy is down, Claude Code hooks fail open - commands execute without any trap checking. The shell wrapper (`claude()` function) mitigates this by only setting `ANTHROPIC_BASE_URL` when the proxy health check passes. If the proxy is unreachable, Claude Code talks directly to Anthropic with no interception at all. The trap file mechanism (`~/.agentsaegis/traps/*.json`) exists as a fallback for detecting active traps even if the hook HTTP call fails. Any future developer must preserve this fail-open guarantee - the proxy must never prevent Claude Code from working.

- **Trap commands must be inherently safe.** `ValidateTrapSafety()` rejects commands that could cause real harm. All `rm` targets must be under `/tmp/.aegis-trap*`, all network destinations must be `0.0.0.0` (connection refused), all packages must use `aegis-trap-nonexistent` prefix, all git operations must use `--dry-run` or `aegis-nonexistent-remote`. If you add a trap that fails safety validation, it's silently dropped at startup.

- **Accept-Encoding is stripped from upstream requests** (handler.go:128). Without this, Anthropic sends gzip-compressed SSE streams that the proxy can't parse for trap injection.

- **Only one active trap at a time.** `Engine.ShouldInject()` returns false while there's an active trap or pending injection. The `pendingInject` flag prevents TOCTOU races between `ShouldInject()` and `SetActiveTrap()`.

- **Trap resolution is idempotent.** `ActiveTrap.Resolved` is an `atomic.Bool` - the first call to `ResolveTrap()` wins, subsequent calls are no-ops. Both the hook path and request-body path can detect resolution, so this prevents double-reporting.

- **Hook cooldown.** After a trap is resolved via the hook, `HookHandler` suppresses the next 10 commands (`hookCooldownCommands`) to avoid re-blocking related commands in the same sequence.

- **SSE buffering.** The `StreamInterceptor` buffers ALL events for a bash tool_use content block until `content_block_stop`. Non-bash blocks pass through immediately. If injection fails, buffered events are flushed unchanged.

- **Scanner buffer size.** SSE scanner uses a 1MB max buffer (`handler.go:159`) for large payloads. If an SSE event exceeds this, the scanner will error.

- **Config file location is hardcoded** to `~/.agentsaegis/config.yaml`. The `AEGIS_` env prefix overrides config values but there's no CLI flag to specify a different config path.

- **SSE panic recovery.** The SSE interceptor runs inside `processSSEStream()` with `defer recover()`. If the interceptor panics, remaining upstream data is passed through unmodified via `drainSSEPassthrough()`. The proxy never crashes from a bad trap injection.

- **The shell wrapper uses `claude()` and `copilot()` functions**, not aliases or env exports. `claude()` sets `ANTHROPIC_BASE_URL`, `copilot()` sets `HTTPS_PROXY`. Other tools using these APIs won't be proxied unless they also use the relevant env var.

- **Trap result detection checks content, not is_error.** `checkForTrapResult()` scans the tool_result content for rejection phrases ("was rejected", "doesn't want to proceed"). It does NOT use the `is_error` boolean because trap commands always fail with `is_error: true` (nonexistent targets). This was fixed in v0.5.1.

- **`--super-debug` mode** injects a trap on every single bash command and auto-clears stale traps. It also disables cooldown and jitter on the hook handler. Use it for testing trap injection/detection mechanics.

- **Trap templates are embedded at compile time** via `go:embed all:traps` in `templates.go`. Changes to YAML files require recompilation. There's no runtime template loading.

- **The `expired` result is mapped to `missed`** when reporting to the dashboard API (callback.go:123) because the DB constraint only allows missed/caught/edited.

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
