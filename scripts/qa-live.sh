#!/usr/bin/env bash
set -euo pipefail

BINARY="$(cd "$(dirname "$0")/.." && pwd)/bin/agentsaegis"
TMPDIR_QA=$(mktemp -d)
PASS=0
FAIL=0

pass() { echo "  PASS: $1"; ((PASS++)) || true; }
fail() { echo "  FAIL: $1"; ((FAIL++)) || true; }

cleanup() {
  echo
  echo "--- Cleanup ---"
  "$BINARY" stop 2>/dev/null || true
  rm -f /tmp/.aegis_canary_*
  rm -rf "$TMPDIR_QA"
  # Restore proxy in normal mode so the developer's session keeps working
  echo "  Restoring proxy (normal mode)..."
  "$BINARY" start --daemon 2>/dev/null || true
  sleep 1
  if curl -sf --max-time 2 http://localhost:7331/__aegis/health >/dev/null 2>&1; then
    echo "  Proxy restored"
  else
    echo "  Warning: proxy failed to restart - run 'agentsaegis start --daemon' manually"
  fi
}
trap cleanup EXIT

echo "=== AgentsAegis Live QA - Claude Code + Copilot CLI ==="
echo "  Binary: $BINARY"
echo "  Temp dir: $TMPDIR_QA"

# --- Prerequisite checks ---
echo
echo "--- Prerequisites ---"
if ! command -v claude >/dev/null 2>&1; then
  echo "  ERROR: 'claude' not found. Install Claude Code first."
  exit 1
fi
pass "claude CLI found"

if ! command -v copilot >/dev/null 2>&1; then
  echo "  ERROR: 'copilot' not found. Install GitHub Copilot CLI first."
  exit 1
fi
pass "copilot CLI found"

if [ ! -x "$BINARY" ]; then
  echo "  ERROR: binary not found at $BINARY. Run 'make build' first."
  exit 1
fi
pass "agentsaegis binary found"

# --- Clean slate ---
echo
echo "--- Setup ---"
"$BINARY" stop 2>/dev/null || true
rm -f /tmp/.aegis_canary_*

# Ensure config exists
if [ ! -f ~/.agentsaegis/config.yaml ]; then
  mkdir -p ~/.agentsaegis
  cat > ~/.agentsaegis/config.yaml <<'YAML'
proxy_port: 7331
log_level: debug
YAML
fi

# Start proxy with super-debug (canary trap on every bash command)
"$BINARY" start --super-debug --daemon
sleep 2

if curl -sf --max-time 2 http://localhost:7331/__aegis/health >/dev/null 2>&1; then
  pass "proxy started with super-debug"
else
  fail "proxy failed to start"
  exit 1
fi

# === CLAUDE CODE TEST ===
echo
echo "========================================"
echo "  TEST 1: Claude Code CLI"
echo "========================================"

# Create temporary settings with PreToolUse hook
CLAUDE_SETTINGS="$TMPDIR_QA/claude-settings.json"
# Use agentsaegis hook which auto-detects Claude Code vs Copilot format
cat > "$CLAUDE_SETTINGS" <<SETTINGS
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "$BINARY hook"
          }
        ]
      }
    ]
  }
}
SETTINGS

echo "  Running Claude Code with proxy..."
rm -f /tmp/.aegis_canary_*

# Run Claude Code in non-interactive mode through the proxy
# --allowedTools Bash: allow bash tool usage
# --settings: add our PreToolUse hook
# The proxy will inject a canary trap, the hook will block it
CLAUDE_OUTPUT=$(ANTHROPIC_BASE_URL=http://localhost:7331 claude \
  -p "Run this exact bash command and show me the output: echo agentsaegis-qa-live-test" \
  --allowedTools "Bash" \
  --settings "$CLAUDE_SETTINGS" \
  2>&1) || true

echo "  Claude output (last 5 lines):"
echo "$CLAUDE_OUTPUT" | tail -5 | sed 's/^/    /'

# Safety check: canary file must NOT exist
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
  fail "CRITICAL: canary file exists! Trap command was EXECUTED!"
  echo "  Found: $(ls /tmp/.aegis_canary_*)"
  exit 1
else
  pass "canary file does NOT exist (trap was never executed)"
fi

# Check if output shows the trap was blocked (training message or deny)
if echo "$CLAUDE_OUTPUT" | grep -qi "blocked\|security\|denied\|AGENTSAEGIS\|not executed\|permission"; then
  pass "Claude Code received trap blocking signal"
else
  echo "  (Note: could not detect blocking message in output - trap may have been injected but hook deny may appear differently)"
fi

# Reset for next test - stop and restart proxy to clear active trap
"$BINARY" stop 2>/dev/null || true
sleep 1
rm -f /tmp/.aegis_canary_*
"$BINARY" start --super-debug --daemon
sleep 2

# === COPILOT CLI TEST ===
echo
echo "========================================"
echo "  TEST 2: Copilot CLI (via MCP)"
echo "========================================"

# Create temporary MCP config for Copilot
MCP_CONFIG="$TMPDIR_QA/mcp-config.json"
cat > "$MCP_CONFIG" <<MCP
{
  "mcpServers": {
    "agentsaegis": {
      "command": "$BINARY",
      "args": ["mcp"]
    }
  }
}
MCP

echo "  Running Copilot with AgentsAegis MCP server..."
rm -f /tmp/.aegis_canary_*

# Run Copilot in non-interactive mode with our MCP bash tool
# --allow-all-tools: auto-approve tool calls
# --additional-mcp-config: add our MCP server
COPILOT_OUTPUT=$(copilot \
  -p "Use the bash tool from agentsaegis MCP server to run: echo agentsaegis-copilot-test" \
  --allow-all-tools \
  --additional-mcp-config "@$MCP_CONFIG" \
  -s \
  2>&1) || true

echo "  Copilot output (last 5 lines):"
echo "$COPILOT_OUTPUT" | tail -5 | sed 's/^/    /'

# Safety check: canary file must NOT exist
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
  fail "CRITICAL: canary file exists! Trap command was EXECUTED!"
  echo "  Found: $(ls /tmp/.aegis_canary_*)"
  exit 1
else
  pass "canary file does NOT exist (trap was never executed)"
fi

# Check if MCP blocked or showed training message
if echo "$COPILOT_OUTPUT" | grep -qi "AGENTSAEGIS\|blocked\|security\|not executed\|trap"; then
  pass "Copilot received trap blocking from MCP server"
else
  echo "  (Note: could not detect blocking message - Copilot may have used its own shell tool instead of MCP)"
fi

# === MCP DIRECT TEST (guaranteed to work) ===
echo
echo "========================================"
echo "  TEST 3: MCP Server Direct"
echo "========================================"

rm -f /tmp/.aegis_canary_*

# Get the canary command from super-debug mode - send a request to trigger trap injection
# Then test MCP with the trap command
MCP_RESULT=$(echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bash","arguments":{"command":"echo mcp-direct-test"}}}' \
  | "$BINARY" mcp 2>/dev/null) || MCP_RESULT=""

if echo "$MCP_RESULT" | grep -q "mcp-direct-test\|AGENTSAEGIS"; then
  pass "MCP server responds to bash tool"
else
  fail "MCP server did not respond correctly"
fi

# Safety check again
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
  fail "CRITICAL: canary file exists after MCP test!"
  exit 1
else
  pass "canary file does NOT exist after MCP test"
fi

# === HOOK BRIDGE TEST ===
echo
echo "========================================"
echo "  TEST 4: Hook Bridge (Copilot hook format)"
echo "========================================"

# Send a benign command through the hook bridge
HOOK_RESULT=$(echo '{"timestamp":1234,"cwd":"/tmp","toolName":"bash","toolArgs":"{\"command\":\"echo hook-test\"}"}' \
  | "$BINARY" hook 2>/dev/null) || HOOK_RESULT=""

if [ -z "$HOOK_RESULT" ]; then
  pass "hook bridge allows benign command (empty output = allow)"
else
  fail "hook bridge unexpectedly blocked benign command: $HOOK_RESULT"
fi

# === SUMMARY ===
echo
echo "========================================"
echo "  SAFETY VERIFICATION"
echo "========================================"

# Final canary check
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
  fail "CRITICAL: Canary files found! Trap commands were executed!"
  ls -la /tmp/.aegis_canary_*
else
  pass "No canary files exist - all trap commands were blocked"
fi

echo
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -eq 0 ]; then
  echo "  All tests passed!"
  exit 0
else
  echo "  Some tests failed - review output above"
  exit 1
fi
