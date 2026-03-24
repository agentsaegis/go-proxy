#!/usr/bin/env bash
set -euo pipefail

BINARY="./bin/agentsaegis"
PASS=0
FAIL=0

pass() { echo "  PASS: $1"; ((PASS++)) || true; }
fail() { echo "  FAIL: $1"; ((FAIL++)) || true; }

echo "=== AgentsAegis QA - Full Install Cycle ==="

# --- Cleanup ---
echo
echo "--- Cleanup ---"
"$BINARY" stop 2>/dev/null || true
"$BINARY" remove-shell 2>/dev/null || true
"$BINARY" remove-desktop 2>/dev/null || true
rm -rf ~/.agentsaegis
echo "  Clean slate ready"

# --- Build ---
echo
echo "--- Build ---"
make build
if [ -x "$BINARY" ]; then pass "binary built"; else fail "binary built"; fi

# --- Init (offline) ---
echo
echo "--- Init ---"
"$BINARY" init --offline
if [ -f ~/.agentsaegis/config.yaml ]; then pass "config created"; else fail "config created"; fi

# --- Verify Setup ---
echo
echo "--- Verify Setup ---"

# Shell wrapper
if grep -q "agentsaegis" ~/.zshrc 2>/dev/null || grep -q "agentsaegis" ~/.bashrc 2>/dev/null; then
  pass "shell wrapper installed"
else
  fail "shell wrapper installed"
fi

# Desktop MCP (only if Claude Desktop is installed)
if [ -d "/Applications/Claude.app" ]; then
  CONFIG="$HOME/Library/Application Support/Claude/claude_desktop_config.json"
  if grep -q "agentsaegis" "$CONFIG" 2>/dev/null; then
    pass "desktop MCP configured"
  else
    fail "desktop MCP configured"
  fi
fi

# Proxy running
sleep 1
if curl -sf --max-time 2 http://localhost:7331/__aegis/health >/dev/null 2>&1; then
  pass "proxy running"
else
  fail "proxy running"
fi

# --- CLI Flow ---
echo
echo "--- CLI Flow ---"

# Proxy should accept and forward requests (will get upstream error since no real Anthropic, but any HTTP response is fine)
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:7331/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test-key" \
  -d '{"model":"claude-sonnet-4-20250514","max_tokens":100,"messages":[{"role":"user","content":"test"}]}' 2>/dev/null) || HTTP_CODE="000"
if [ "$HTTP_CODE" != "000" ]; then
  pass "proxy accepts requests (HTTP $HTTP_CODE)"
else
  fail "proxy accepts requests"
fi

# Hook endpoint should respond (allow when no active trap)
HOOK_CODE=$(curl -sf -o /dev/null -w "%{http_code}" -X POST http://localhost:7331/hooks/pre-tool-use \
  -H "Content-Type: application/json" \
  -d '{"session_id":"qa","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"tool_use_id":"qa1"}' 2>/dev/null) || HOOK_CODE="000"
if [ "$HOOK_CODE" = "200" ]; then
  pass "hook endpoint responds"
else
  fail "hook endpoint responds (got $HOOK_CODE)"
fi

# --- MCP Flow ---
echo
echo "--- MCP Flow ---"

# MCP initialize
MCP_RESP=$(echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"qa","version":"1.0"}}}' \
  | "$BINARY" mcp 2>/dev/null) || MCP_RESP=""
if echo "$MCP_RESP" | grep -q "agentsaegis"; then
  pass "MCP initialize"
else
  fail "MCP initialize"
fi

# MCP tools/list
MCP_TOOLS=$(echo '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | "$BINARY" mcp 2>/dev/null) || MCP_TOOLS=""
if echo "$MCP_TOOLS" | grep -q '"bash"'; then
  pass "MCP tools/list has bash"
else
  fail "MCP tools/list has bash"
fi

# MCP bash execution
MCP_EXEC=$(echo '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bash","arguments":{"command":"echo qa-test-ok"}}}' \
  | "$BINARY" mcp 2>/dev/null) || MCP_EXEC=""
if echo "$MCP_EXEC" | grep -q "qa-test-ok"; then
  pass "MCP bash execution"
else
  fail "MCP bash execution"
fi

# --- Uninstall ---
echo
echo "--- Uninstall ---"

"$BINARY" stop 2>/dev/null || true
sleep 1
if ! curl -sf --max-time 1 http://localhost:7331/__aegis/health >/dev/null 2>&1; then
  pass "proxy stopped"
else
  fail "proxy stopped"
fi

"$BINARY" remove-shell 2>/dev/null || true
if ! grep -q "agentsaegis" ~/.zshrc 2>/dev/null && ! grep -q "agentsaegis" ~/.bashrc 2>/dev/null; then
  pass "shell wrapper removed"
else
  fail "shell wrapper removed"
fi

if [ -d "/Applications/Claude.app" ]; then
  "$BINARY" remove-desktop 2>/dev/null || true
  if ! grep -q "agentsaegis" "$CONFIG" 2>/dev/null; then
    pass "desktop MCP removed"
  else
    fail "desktop MCP removed"
  fi
fi

rm -rf ~/.agentsaegis

# --- Restore working state ---
echo
echo "--- Restore ---"
"$BINARY" init --offline
echo "  Environment restored"

# --- Summary ---
echo
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -eq 0 ]; then
  exit 0
else
  exit 1
fi
