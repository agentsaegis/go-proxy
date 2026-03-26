#!/usr/bin/env bash
# qa-docker-entrypoint.sh - Container entrypoint for Docker QA
set -euo pipefail

TARGET="${TEST_TARGET:-all}"
PASS=0
FAIL=0

echo "=== AgentsAegis Docker QA ==="
echo "Target: $TARGET"
echo

# Set up home directory for qa user
mkdir -p /home/qa/.agentsaegis
cat > /home/qa/.agentsaegis/config.yaml <<EOF
proxy_port: 7331
log_level: debug
EOF
chown -R qa:qa /home/qa/.agentsaegis

# Start proxy as qa user in super-debug mode
echo "[entrypoint] Starting proxy daemon..."
gosu qa agentsaegis start --super-debug --daemon

# Wait for health check
echo "[entrypoint] Waiting for proxy health..."
DEADLINE=10
ELAPSED=0
while true; do
    if curl -sf http://localhost:7331/__aegis/health >/dev/null 2>&1; then
        echo "[entrypoint] Proxy is healthy."
        break
    fi
    if [ "$ELAPSED" -ge "$DEADLINE" ]; then
        echo "[entrypoint] ERROR: Proxy did not become healthy within ${DEADLINE}s."
        if [ -f /home/qa/.agentsaegis/log ]; then
            echo "--- proxy log ---"
            tail -50 /home/qa/.agentsaegis/log
        fi
        exit 1
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done

# Trust proxy CA cert (runs as root - direct copy, no sudo needed)
echo "[entrypoint] Trusting proxy CA cert..."
CA_PATH=/home/qa/.agentsaegis/ca.pem
if [ -f "$CA_PATH" ]; then
    cp "$CA_PATH" /usr/local/share/ca-certificates/agentsaegis.crt
    update-ca-certificates
    echo "[entrypoint] CA cert trusted."
else
    echo "[entrypoint] ERROR: CA cert not found at $CA_PATH"
    echo "  The proxy should have generated it during startup."
    echo "  Check proxy logs:"
    tail -20 /home/qa/.agentsaegis/log 2>/dev/null || true
    exit 1
fi
echo

# Run target tests
if [ "$TARGET" = "claude" ] || [ "$TARGET" = "all" ]; then
    echo "=== Running Claude Code QA tests ==="
    if gosu qa qa-docker-claude.sh; then
        PASS=$((PASS + 1))
        echo "[claude] PASS"
    else
        FAIL=$((FAIL + 1))
        echo "[claude] FAIL"
    fi
    echo
fi

if [ "$TARGET" = "copilot" ] || [ "$TARGET" = "all" ]; then
    echo "=== Running Copilot CLI QA tests ==="
    if gosu qa qa-docker-copilot.sh; then
        PASS=$((PASS + 1))
        echo "[copilot] PASS"
    else
        FAIL=$((FAIL + 1))
        echo "[copilot] FAIL"
    fi
    echo
fi

# Summary
echo "=== Docker QA Summary ==="
echo "Passed: $PASS"
echo "Failed: $FAIL"

if [ "$FAIL" -gt 0 ]; then
    echo "Result: FAIL"
    exit 1
fi
echo "Result: PASS"
exit 0
