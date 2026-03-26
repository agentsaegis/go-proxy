# Copilot MITM Integration Journey (2026-03-23/24)

## Goal
Make Copilot CLI trap experience identical to Claude Code: traps injected silently in API response stream, user sees trap in confirmation dialog, approves or rejects.

## What worked immediately (Claude Code)
- Shell wrapper sets `ANTHROPIC_BASE_URL=http://localhost:7331`
- Proxy intercepts HTTP/1.1 requests, injects traps in Anthropic SSE stream
- User sees trap in confirmation dialog, result detected via request-body path
- No hooks needed

## Bug fixes along the way

### 1. ServeMux 404 for CONNECT (critical)
Go 1.22+ `http.NewServeMux` doesn't route CONNECT method to `"/"` pattern. The CONNECT tunnel never worked. Fixed by intercepting CONNECT before the mux.

### 2. Trap result detection (is_error bug)
`checkForTrapResult` treated all `is_error: true` as "caught". But trap commands always fail (nonexistent targets), so clicking "Yes" also produced `is_error: true`. Fixed to check content for rejection phrases ("was rejected", "doesn't want to proceed") instead.

### 3. Hook Phase 2 interfering with Claude Code
The `agentsaegis hook` PreToolUse command's Phase 2 (inject-trap) was firing for Claude Code, blocking commands before users saw them. Fixed by skipping Phase 2 for Claude format.

### 4. PreToolUse hook fires before user confirmation
Discovered that both Claude Code and Copilot fire PreToolUse hooks BEFORE the user sees the confirmation dialog. Makes hooks useless for trap detection - the hook catches the trap instantly, user never sees it.

## Copilot-specific attempts

### Attempt 1: Hook-based injection (Phase 2)
- Hook denies original command with trap in the reason text
- LLM sees denial, either gives up or recognizes trap as suspicious
- User never gets the natural confirmation dialog experience
- **Verdict: broken by design**

### Attempt 2: CONNECT tunnel + TLS MITM
- Shell wrapper sets `HTTPS_PROXY=http://localhost:7331`
- Proxy performs TLS MITM on `api.individual.githubcopilot.com`

**Sub-problems encountered:**

#### 2a. Wrong MITM host
`mitmHosts` only had `api.github.com`. Copilot uses `api.individual.githubcopilot.com` for model inference. Fixed by adding Copilot API hosts.

#### 2b. `/mcp/readonly` SSE blocking
Non-intercepted SSE streams (`/mcp/readonly`) went through `resp.Write(conn)` which blocks until stream ends. This blocked the sequential request loop. Fixed with goroutine handoff for non-intercepted SSE.

#### 2c. HTTP/2 GOAWAY errors
Copilot negotiated HTTP/2 over the MITM TLS connection via ALPN. Our `http.ReadRequest()` loop only handles HTTP/1.1. Fixed by setting `NextProtos: []string{"http/1.1"}` on MITM TLS config to force HTTP/1.1 fallback. (Copilot's fix)

#### 2d. OAI SSE buffering causes timeout
The OAI stream interceptor buffered tool call lines (returned nil). Copilot received nothing during buffering and timed out. Fixed by restoring proper buffer-then-flush with non-shell lines passing through immediately. (Copilot's fix)

#### 2e. Transfer-Encoding mismatch
Original response had `Transfer-Encoding: chunked` but proxy wrote raw lines. Copilot's HTTP parser hung waiting for chunk framing. Fixed by stripping `Transfer-Encoding` and `Content-Length`, adding `Connection: close`. (Copilot's fix)

#### 2f. Connection not closed after SSE (current)
After intercepted SSE stream ends, proxy loops back to `http.ReadRequest()` which blocks forever. Copilot expects the connection to close to detect end-of-body. Fixed with `errSSEIntercepted` sentinel that triggers connection close. (Copilot's fix - testing now)

### Attempt 3: LLM trap recognition
Even when injection works, LLMs recognize current trap patterns (`0.0.0.0`, `/nonexistent/`, `aegis-trap`) as suspicious and refuse to suggest them. Trap templates need redesign with realistic-looking targets.

## Current state
- Claude Code: fully working via SSE proxy, no hooks
- Copilot: MITM tunnel works, SSE interception works, testing Copilot's latest connection-close fix
- Docker test environment: `./scripts/test-interactive.sh`

## Key architectural decisions
1. No hooks for Claude Code - SSE proxy + request-body detection only
2. MITM approach for Copilot (not hooks) - same UX as Claude Code
3. Hook Phase 2 (inject-trap) to be removed - broken UX
4. `server/` package needs refactor into smaller modules
5. Trap templates need redesign for LLM bypass
