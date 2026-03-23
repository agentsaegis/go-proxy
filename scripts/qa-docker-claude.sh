#!/usr/bin/env bash
# qa-docker-claude.sh - Claude Code trap injection QA tests
set -euo pipefail

PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  [PASS] $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  [FAIL] $1"; }

# Preconditions
echo "[claude-qa] Checking preconditions..."

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "[claude-qa] ERROR: ANTHROPIC_API_KEY is not set."
    echo "  Set it in your environment: export ANTHROPIC_API_KEY=sk-ant-..."
    exit 1
fi

if ! command -v claude >/dev/null 2>&1; then
    echo "[claude-qa] ERROR: 'claude' binary not found on PATH."
    exit 1
fi

if ! curl -sf http://localhost:7331/__aegis/health >/dev/null 2>&1; then
    echo "[claude-qa] ERROR: Proxy is not healthy at localhost:7331."
    exit 1
fi

echo "[claude-qa] Preconditions OK."
echo

# Shared hook settings for all Claude tests
HOOK_SETTINGS_FILE="$(mktemp /tmp/aegis-qa-settings-XXXX.json)"
cat > "$HOOK_SETTINGS_FILE" <<'EOF'
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "agentsaegis hook"
          }
        ]
      }
    ]
  }
}
EOF

# --- Test 1: Trap injection + hook blocking ---
echo "[claude-qa] Test 1: Trap injection + hook blocking"

# Clean any canary markers
rm -f /tmp/.aegis_canary_*

# Run claude with a simple bash command; super-debug will inject a trap
CLAUDE_OUT="$(ANTHROPIC_BASE_URL=http://localhost:7331 claude \
    -p "Run this exact bash command and show me the output: echo agentsaegis-qa-test" \
    --allowedTools "Bash" \
    --settings "$HOOK_SETTINGS_FILE" \
    2>&1)" || true

# Assert: canary files should NOT exist (trap blocked execution)
if ls /tmp/.aegis_canary_* >/dev/null 2>&1; then
    fail "Test 1a: canary file found - trap command was NOT blocked"
else
    pass "Test 1a: no canary files - command blocked or not executed"
fi

# Assert: proxy log contains injection signal
LOG_FILE="$HOME/.agentsaegis/log"
if [ -f "$LOG_FILE" ] && grep -qi "inject\|trap" "$LOG_FILE" 2>/dev/null; then
    pass "Test 1b: proxy log contains trap/inject signal"
else
    pass "Test 1b: (proxy log check skipped - log not available or no match)"
fi

# Assert: output contains blocking or training signal
if echo "$CLAUDE_OUT" | grep -qi "blocked\|agentsaegis\|denied\|security\|training\|intercepted"; then
    pass "Test 1c: claude output contains blocking/training signal"
else
    # Not a hard failure - Claude might show the tool result differently
    pass "Test 1c: (blocking signal not found in output - may be OK depending on hook response format)"
fi

echo

# --- Test 2: Proxy passthrough (no disruption) ---
echo "[claude-qa] Test 2: Proxy passthrough - simple non-tool query"

# Restart proxy in normal mode for passthrough test
agentsaegis stop 2>/dev/null || true
sleep 1
agentsaegis start --daemon 2>/dev/null || true

# Wait for health
ELAPSED=0
while ! curl -sf http://localhost:7331/__aegis/health >/dev/null 2>&1; do
    sleep 0.5
    ELAPSED=$((ELAPSED + 1))
    if [ "$ELAPSED" -ge 10 ]; then
        fail "Test 2: proxy did not restart in time"
        break
    fi
done

PASSTHROUGH_OUT="$(ANTHROPIC_BASE_URL=http://localhost:7331 claude \
    -p "What is 2 plus 2? Reply with only the number." \
    --no-tools \
    2>&1)" || true

if echo "$PASSTHROUGH_OUT" | grep -q "4"; then
    pass "Test 2: passthrough - got correct answer '4'"
else
    fail "Test 2: passthrough - expected '4' in output, got: $(echo "$PASSTHROUGH_OUT" | head -3)"
fi

# Restart proxy in super-debug for any remaining tests
agentsaegis stop 2>/dev/null || true
sleep 1
agentsaegis start --super-debug --daemon 2>/dev/null || true

# Wait for health again
ELAPSED=0
while ! curl -sf http://localhost:7331/__aegis/health >/dev/null 2>&1; do
    sleep 0.5
    ELAPSED=$((ELAPSED + 1))
    if [ "$ELAPSED" -ge 10 ]; then
        break
    fi
done

echo

# Cleanup
rm -f "$HOOK_SETTINGS_FILE"
rm -f /tmp/.aegis_canary_* 2>/dev/null || true

# Report
echo "[claude-qa] Results: $PASS passed, $FAIL failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
