# Live E2E Test Runner Design

## Overview

Automated end-to-end test runner that makes real API calls through the AgentsAegis go-proxy to verify SSE parsing, trap injection, and event reporting across all supported tool/model combinations.

Replaces manual testing of each combination, which currently takes ~1 hour per full pass.

## Goals

- Verify real SSE parsing works with actual API responses (not mocked)
- Cover all tool/model combinations in a single test run
- Verify trap injection appears in the SSE response stream
- Verify event reporting to the real dashboard API (caught/missed/clean)
- Run in minutes via `make test-live`
- Clear pass/fail matrix output

## Non-Goals

- Replacing existing unit tests or mock-based E2E tests
- Testing the CLI commands themselves (already covered by cmd_test.go)
- Testing the dashboard API (that's a separate project)
- Load testing or performance benchmarking

## Architecture

### Format

Go test files in `e2e/` with `//go:build live` build tag. Tests the real compiled binary as an external subprocess - not in-process.

### File Structure

```
e2e/
  live_test.go          - TestMain, matrix runner, summary output
  live_claude_test.go   - Claude CLI test scenarios (Anthropic SSE)
  live_copilot_test.go  - Copilot test scenarios, all models (OpenAI SSE)
  live_helpers_test.go  - Proxy lifecycle, token exchange, SSE parsing, dashboard queries
```

### Makefile Target

```makefile
test-live: build
	go test -race -tags live -v -count=1 -timeout 10m ./e2e/...
```

## Test Matrix

4 providers x 5 scenarios = 20 tests.

### Providers

| Provider | SSE Format | Auth | Proxy Path |
|----------|-----------|------|------------|
| Claude CLI | Anthropic | `ANTHROPIC_API_KEY` header | Reverse proxy to api.anthropic.com |
| Copilot/GPT | OpenAI | Copilot session token | CONNECT tunnel, TLS MITM |
| Copilot/Claude | OpenAI | Copilot session token | CONNECT tunnel, TLS MITM |
| Copilot/Codex | OpenAI | Copilot session token | CONNECT tunnel, TLS MITM |

### Scenarios

| # | Scenario | Proxy Mode | Prompt Type | What to Verify |
|---|----------|-----------|-------------|----------------|
| 1 | Passthrough | Any | Non-code | Valid response, no tool_use, no events on dashboard |
| 2 | Injection | Super-debug | Code | Trap command appears in SSE tool_use block |
| 3 | Approve | Super-debug | Code | Hook deny + "missed" event reported to dashboard |
| 4 | Reject | Super-debug | Code | Hook allow + no "missed" event on dashboard |
| 5 | Clean | No super-debug | Non-code | Valid response, no injection, no events |

## Proxy Lifecycle

### Two-Phase Execution

The proxy runs in two modes. Rather than restarting per test, tests are grouped by mode:

**Phase 1** - Injection tests (super-debug ON):
1. Build binary: `go build -o <tmpdir>/agentsaegis ./cmd/agentsaegis`
2. Start subprocess: `./agentsaegis start --super-debug --debug`
3. Wait for health: `GET http://localhost:<port>/__aegis/health`
4. Run scenarios 2, 3, 4 for all providers
5. SIGTERM + wait

**Phase 2** - Passthrough tests (super-debug OFF):
1. Start subprocess: `./agentsaegis start --debug`
2. Wait for health
3. Run scenarios 1, 5 for all providers
4. SIGTERM + wait

### Subprocess Management

- Temp HOME directory (`t.TempDir()`) isolates config, CA certs, trap files
- Dynamic port via `findFreePort()`
- Env vars: `HOME=<tmpdir>`, `AEGIS_PROXY_PORT=<port>`, `AEGIS_API_TOKEN=<token>`, `AEGIS_DASHBOARD_URL=<url>`
- Auto-generate config.yaml in temp HOME with `agentsaegis init` or direct file write
- Stderr captured to buffer for log output on failure
- `t.Cleanup()` sends SIGTERM, waits 5s, then SIGKILL if needed

## Authentication

### Claude CLI

Straightforward: `ANTHROPIC_API_KEY` env var passed to HTTP request headers.

```
POST http://localhost:<port>/v1/messages
Headers:
  x-api-key: <ANTHROPIC_API_KEY>
  anthropic-version: 2023-06-01
  content-type: application/json
```

### Copilot Token Exchange

VS Code Copilot uses a two-step auth flow:

1. GitHub OAuth token (from `GITHUB_TOKEN` env var or `gh auth token`)
2. Exchange for short-lived Copilot session token

```
func acquireCopilotToken(t *testing.T) (token string, endpoint string) {
    // 1. Get GitHub token
    githubToken := os.Getenv("GITHUB_TOKEN")
    if githubToken == "" {
        // Try: gh auth token
        out, err := exec.Command("gh", "auth", "token").Output()
        if err != nil {
            t.Skip("no GitHub token available, skipping Copilot tests")
        }
        githubToken = strings.TrimSpace(string(out))
    }

    // 2. Exchange for Copilot token (DIRECT, not through proxy)
    req, _ := http.NewRequest("GET", "https://api.github.com/copilot_internal/v2/token", nil)
    req.Header.Set("Authorization", "token "+githubToken)
    resp, err := http.DefaultClient.Do(req)
    // ...

    // 3. Parse response
    // { "token": "...", "expires_at": ..., "endpoints": { "api": "https://..." } }
    // Return token + endpoint URL
}
```

The token exchange bypasses the proxy (direct HTTPS) since it's auth setup. Subsequent Copilot API calls go through the proxy's CONNECT tunnel.

### Copilot CONNECT Tunnel

Copilot API calls must go through the proxy's CONNECT tunnel with TLS MITM:

```
func copilotRequest(proxyAddr, targetHost string, caPool *x509.CertPool) *http.Client {
    // 1. Create HTTP client with proxy CONNECT support
    // 2. Custom TLS config trusting proxy's generated CA
    // 3. Proxy dials CONNECT to targetHost:443
    // 4. Proxy MITMs TLS, intercepts SSE
}
```

### TLS Trust

The proxy generates a CA cert on first start at `$HOME/.agentsaegis/ca.pem`. After health check passes:

```
func loadProxyCA(t *testing.T, homeDir string) *x509.CertPool {
    caPEM, _ := os.ReadFile(filepath.Join(homeDir, ".agentsaegis", "ca.pem"))
    pool := x509.NewCertPool()
    pool.AppendCertsFromPEM(caPEM)
    return pool
}
```

## Test Scenarios - Detailed

### Prompts

Minimizing token cost (real API calls cost money):

- **Code prompt**: `"Write a single bash command to list files in /tmp. Use the bash tool."` - triggers bash tool_use
- **Non-code prompt**: `"Reply with just the word 'hello'. Do not use any tools."` - no tool_use, minimal tokens
- `max_tokens: 256` for all requests

### Scenario 1: Passthrough

1. Send non-code prompt through proxy
2. Parse SSE response
3. Assert: valid, parseable response
4. Assert: no `tool_use` blocks with type `bash`
5. Assert: no events on dashboard for this session ID

### Scenario 2: Injection

1. Send code prompt through proxy (super-debug forces injection)
2. Parse SSE response, collect all `tool_use` blocks
3. Assert: at least one `tool_use` block exists with `name: "bash"` (or equivalent for OpenAI format)
4. Extract the bash command from the tool_use input
5. Assert: command contains trap markers (canary template in super-debug uses known patterns)

For Claude (Anthropic SSE format):
- Look for `content_block_start` with `type: "tool_use"`, `name: "bash"`
- Accumulate `content_block_delta` events for the input JSON
- Parse accumulated input as `{"command": "..."}`

For Copilot (OpenAI SSE format):
- Look for `choices[0].delta.tool_calls` with function name
- Accumulate `arguments` deltas
- Parse accumulated arguments as `{"command": "..."}`

### Scenario 3: Approve (developer runs trap command)

1. Send code prompt, extract injected trap command from SSE response
2. Send hook request simulating developer approval:
   ```
   POST http://localhost:<port>/hooks/pre-tool-use
   {
     "session_id": "<unique_test_session>",
     "hook_event_name": "PreToolUse",
     "tool_name": "Bash",
     "tool_input": {"command": "<trap_command>"},
     "tool_use_id": "<tool_use_id_from_response>"
   }
   ```
3. Assert: hook returns deny response (command blocked)
4. Query dashboard API for events matching this session
5. Assert: event exists with `result: "missed"` and matching trap template ID

### Scenario 4: Reject (developer edits command)

1. Send code prompt, extract injected trap command from SSE response
2. Send hook request with a DIFFERENT command:
   ```
   POST http://localhost:<port>/hooks/pre-tool-use
   {
     "session_id": "<unique_test_session>",
     "hook_event_name": "PreToolUse",
     "tool_name": "Bash",
     "tool_input": {"command": "ls -la"},
     "tool_use_id": "<tool_use_id_from_response>"
   }
   ```
3. Assert: hook returns allow response (command passes through)
4. Query dashboard API
5. Assert: no "missed" event for this session

### Scenario 5: Clean Flow

1. Send non-code prompt (super-debug OFF - but doesn't matter since no tool_use)
2. Parse SSE response
3. Assert: valid response, no tool_use blocks
4. Query dashboard: no events for this session

## Event Verification

The dashboard API needs a query endpoint for individual events. The test runner will call it to verify events were (or were not) recorded.

**Required dashboard endpoint** (to be built):
```
GET /api/proxy/events?session_id=<id>
Authorization: Bearer <token>

Response: {
  "events": [
    {
      "trap_template_id": "...",
      "trap_category": "...",
      "result": "missed",
      "session_id": "...",
      "created_at": "..."
    }
  ]
}
```

**Verification helper:**
```
func queryDashboardEvents(t *testing.T, dashboardURL, token, sessionID string) []TrapEvent {
    // GET /api/proxy/events?session_id=<id>
    // Retry with backoff (event reporting is async via goroutine)
    // Return parsed events
}
```

**Polling**: Event reporting in the proxy is fire-and-forget (goroutine). The test needs to poll the dashboard with a short backoff (e.g., 100ms intervals, 5s timeout) to wait for async event delivery.

## Output Format

### During Execution

Standard Go test output with subtests:

```
=== RUN   TestLive
=== RUN   TestLive/SuperDebug
=== RUN   TestLive/SuperDebug/Claude/Injection
--- PASS: TestLive/SuperDebug/Claude/Injection (2.31s)
=== RUN   TestLive/SuperDebug/Claude/Approve
--- PASS: TestLive/SuperDebug/Claude/Approve (1.85s)
...
```

### Summary Matrix

Printed by TestMain after all tests complete:

```
Live E2E Test Results
=====================
Provider        | Passthrough | Injection | Approve | Reject | Clean
Claude CLI      |    PASS     |   PASS    |  PASS   |  PASS  | PASS
Copilot/GPT     |    PASS     |   PASS    |  PASS   |  PASS  | PASS
Copilot/Claude  |    PASS     |   PASS    |  PASS   |  PASS  | PASS
Copilot/Codex   |    SKIP     |   SKIP    |  SKIP   |  SKIP  | SKIP

3/4 providers tested, 15/15 passed, 0 failed
Duration: 2m34s
Tokens used: ~12,000 (estimated)
```

### Failure Output

On failure, dump:
- The specific assertion that failed
- Relevant SSE events (last N events before failure)
- Proxy stderr log (captured from subprocess)
- Request/response details

## Skip Logic

- No `ANTHROPIC_API_KEY` -> skip all Claude tests
- No `GITHUB_TOKEN` and no `COPILOT_TOKEN` -> skip all Copilot tests
- Copilot token exchange fails -> skip Copilot tests with reason
- Specific model rejected by Copilot API (e.g., model not available) -> skip that model's tests
- All tests skipped -> test passes with warning (not a failure)

## Constraints

- **Cost**: Real API calls cost money. Short prompts + `max_tokens: 256` minimize usage. Estimated ~3,000 tokens per provider per full run.
- **Rate limits**: Sequential requests per provider to avoid rate limiting. No parallelism across providers (Copilot rate limits are per-token).
- **Flakiness**: LLM responses are non-deterministic. Code prompts may not always produce bash tool_use. Retry logic: up to 3 attempts for injection tests. Non-code prompts should reliably avoid tool_use.
- **Network**: Requires internet access to real APIs. Not suitable for CI without API key secrets.
- **Copilot model availability**: Some models may not be available for all Copilot plans. Tests skip gracefully.

## Dependencies

- Existing: go build toolchain, `internal/trap`, `internal/server`, `internal/client`
- New: dashboard query endpoint for individual events (to be built separately)
- External: `gh` CLI (optional, for Copilot token extraction fallback)

## Future Extensions

- CI integration with secret management for API keys
- Token usage tracking and cost reporting
- Performance benchmarking (SSE latency through proxy vs direct)
- Additional model combinations as Copilot adds models
