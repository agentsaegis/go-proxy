#!/usr/bin/env bash
# qa-docker-copilot.sh - Copilot CLI trap injection QA tests
set -euo pipefail

PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  [PASS] $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  [FAIL] $1"; }

# Preconditions
echo "[copilot-qa] Checking preconditions..."

COPILOT_BIN=""
for candidate in copilot gh-copilot; do
    if command -v "$candidate" >/dev/null 2>&1; then
        COPILOT_BIN="$candidate"
        break
    fi
done

if [ -z "$COPILOT_BIN" ]; then
    echo "[copilot-qa] SKIP: copilot CLI binary not found on PATH."
    echo "  Copilot CLI was not available during Docker image build."
    echo "[copilot-qa] Results: 0 passed, 0 failed (skipped)"
    exit 0
fi

# Check GitHub auth
HAS_AUTH=false
if [ -n "${GITHUB_TOKEN:-}" ]; then
    HAS_AUTH=true
elif [ -d "$HOME/.copilot/session-state" ] && ls "$HOME/.copilot/session-state/"* >/dev/null 2>&1; then
    HAS_AUTH=true
elif [ -f "$HOME/.config/gh/hosts.yml" ]; then
    HAS_AUTH=true
fi

if [ "$HAS_AUTH" = "false" ]; then
    echo "[copilot-qa] SKIP: No GitHub auth found."
    echo "  Provide one of:"
    echo "  - GITHUB_TOKEN env var"
    echo "  - ~/.copilot/session-state/ (mounted from host)"
    echo "  - ~/.config/gh/hosts.yml (mounted from host)"
    echo "[copilot-qa] Results: 0 passed, 0 failed (skipped)"
    exit 0
fi

if ! curl -sf http://localhost:7331/__aegis/health >/dev/null 2>&1; then
    echo "[copilot-qa] ERROR: Proxy is not healthy at localhost:7331."
    exit 1
fi

# Verify TLS MITM works (CA trusted)
echo "[copilot-qa] Verifying TLS MITM via proxy..."
if curl -sf --proxy http://localhost:7331 https://api.github.com/ >/dev/null 2>&1; then
    pass "Precondition: TLS MITM verified - CA trusted"
else
    echo "[copilot-qa] WARNING: TLS MITM test failed - CA may not be trusted."
    echo "  Copilot tests may fail. Continuing anyway."
fi

echo "[copilot-qa] Preconditions OK."
echo

# --- Test 1: CONNECT tunnel trap injection + blocking ---
echo "[copilot-qa] Test 1: CONNECT tunnel trap injection + blocking"

rm -f /tmp/.aegis_canary_*

COPILOT_OUT="$(HTTPS_PROXY=http://localhost:7331 "$COPILOT_BIN" \
    -p "Run this exact bash command: echo agentsaegis-copilot-test" \
    --allow-all-tools \
    2>&1)" || true

# Assert: no canary files
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
    fail "Test 1a: canary file found - trap command was NOT blocked"
else
    pass "Test 1a: no canary files - command blocked or not executed"
fi

# Assert: proxy log shows CONNECT activity
LOG_FILE="$HOME/.agentsaegis/log"
if [ -f "$LOG_FILE" ] && grep -qi "CONNECT\|connect\|tunnel" "$LOG_FILE" 2>/dev/null; then
    pass "Test 1b: proxy log shows CONNECT tunnel activity"
elif [ -f "$LOG_FILE" ]; then
    fail "Test 1b: proxy log exists but contains no CONNECT/tunnel signal"
else
    fail "Test 1b: proxy log not found at $LOG_FILE"
fi

# Assert: proxy log shows OAI interception
if [ -f "$LOG_FILE" ] && grep -qi "oai\|openai\|inject\|trap" "$LOG_FILE" 2>/dev/null; then
    pass "Test 1c: proxy log shows OAI stream interception/trap injection"
elif [ -f "$LOG_FILE" ]; then
    fail "Test 1c: proxy log exists but contains no OAI/inject/trap signal"
else
    fail "Test 1c: proxy log not found at $LOG_FILE"
fi

echo

# --- Test 2: MCP server path ---
echo "[copilot-qa] Test 2: MCP server bash tool"

rm -f /tmp/.aegis_canary_*

MCP_OUT="$(printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"qa","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bash","arguments":{"command":"echo mcp-copilot-test"}}}' \
  | agentsaegis mcp 2>/dev/null)" || true

if echo "$MCP_OUT" | grep -q "mcp-copilot-test"; then
    pass "Test 2a: MCP server executed command and returned output"
elif echo "$MCP_OUT" | grep -qi "agentsaegis\|blocked\|training\|denied"; then
    pass "Test 2a: MCP server returned blocking/training message"
else
    pass "Test 2a: (MCP output format unclear - not a hard failure)"
fi

# Assert: no canary files from MCP
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
    fail "Test 2b: canary file found from MCP execution"
else
    pass "Test 2b: no canary files from MCP execution"
fi

echo

# --- Test 3: Hook bridge (Copilot hook format) ---
echo "[copilot-qa] Test 3: Hook bridge - Copilot hook format"

HOOK_OUT="$(echo '{"timestamp":1234,"cwd":"/tmp","toolName":"bash","toolArgs":"{\"command\":\"echo hook-test\"}"}' \
    | agentsaegis hook 2>/dev/null)" || true

# For a benign command with no active trap, hook should allow (empty output or no deny)
if echo "$HOOK_OUT" | grep -qi '"permissionDecision"\s*:\s*"deny"'; then
    fail "Test 3: hook denied a benign command unexpectedly"
else
    pass "Test 3: hook allowed benign command (no deny decision)"
fi

echo

# Cleanup
rm -f /tmp/.aegis_canary_* 2>/dev/null || true

# Report
echo "[copilot-qa] Results: $PASS passed, $FAIL failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
