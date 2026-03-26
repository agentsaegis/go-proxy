#!/usr/bin/env bash
# test-interactive.sh - Launch an interactive Docker container for manual trap testing
# Usage: ./scripts/test-interactive.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== AgentsAegis Interactive Test Environment ==="
echo

# Check auth
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "[WARN] ANTHROPIC_API_KEY not set - Claude Code testing will fail"
    echo "       export ANTHROPIC_API_KEY=sk-ant-..."
fi
if [ -z "${GITHUB_TOKEN:-}" ] && [ ! -d "$HOME/.copilot" ]; then
    echo "[WARN] No Copilot auth found - set GITHUB_TOKEN or have ~/.copilot/"
fi
echo

# Build image
echo "--- Building Docker image ---"
docker build -f "$REPO_ROOT/Dockerfile.qa" -t agentsaegis-qa:local "$REPO_ROOT" 2>&1 | tail -3
echo

# Assemble volume mounts
MOUNTS=()
if [ -d "$HOME/.copilot" ]; then
    MOUNTS+=("-v" "$HOME/.copilot:/home/qa/.copilot-host:ro")
fi
if [ -d "$HOME/.config/gh" ]; then
    MOUNTS+=("-v" "$HOME/.config/gh:/home/qa/.config/gh:ro")
fi

echo "--- Starting interactive container ---"
echo "Proxy will start automatically in super-debug mode (trap on every command)."
echo "Shell wrappers for 'claude' and 'copilot' are pre-configured."
echo
echo "Test commands:"
echo "  claude          # Launch Claude Code through proxy"
echo "  copilot         # Launch Copilot CLI through proxy"
echo "  agentsaegis status   # Check proxy status"
echo "  exit            # Leave container"
echo

docker run --rm -it \
    -e "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}" \
    -e "GITHUB_TOKEN=${GITHUB_TOKEN:-}" \
    -e "AEGIS_API_TOKEN=${AEGIS_API_TOKEN:-}" \
    -e "AEGIS_DASHBOARD_URL=${AEGIS_DASHBOARD_URL:-}" \
    "${MOUNTS[@]}" \
    --add-host=host.docker.internal:host-gateway \
    --entrypoint /bin/bash \
    agentsaegis-qa:local \
    -c '
# ---- All of this runs as root ----

# 1. Configure proxy
mkdir -p /home/qa/.agentsaegis
DASHBOARD_URL="${AEGIS_DASHBOARD_URL:-}"
API_TOKEN="${AEGIS_API_TOKEN:-}"
cat > /home/qa/.agentsaegis/config.yaml <<CONF
proxy_port: 7331
log_level: debug
${DASHBOARD_URL:+dashboard_url: $DASHBOARD_URL}
${API_TOKEN:+api_token: $API_TOKEN}
CONF
chown -R qa:qa /home/qa/.agentsaegis

# 2. Copy Copilot auth from host mount (read-only) to writable location
if [ -d /home/qa/.copilot-host ]; then
    cp -r /home/qa/.copilot-host /home/qa/.copilot
    # Remove hook config - traps come via MITM, not hooks
    rm -rf /home/qa/.copilot/hooks
    chown -R qa:qa /home/qa/.copilot
    echo "[setup] Copilot auth copied (hooks removed)."
fi

# 3. Make /workspace writable by qa
chown qa:qa /workspace

# 4. Start proxy as qa user (generates CA cert on first start)
echo "[setup] Starting proxy..."
gosu qa agentsaegis start --super-debug --daemon

# 5. Wait for proxy health + CA cert to be generated
ELAPSED=0
while [ $ELAPSED -lt 10 ]; do
    if curl -sf http://localhost:7331/__aegis/health >/dev/null 2>&1; then
        echo "[setup] Proxy is healthy."
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done

# 6. Trust CA cert (still running as root here!)
CA_PATH=/home/qa/.agentsaegis/ca.pem
if [ -f "$CA_PATH" ]; then
    cp "$CA_PATH" /usr/local/share/ca-certificates/agentsaegis.crt
    update-ca-certificates 2>&1 | tail -1
    echo "[setup] CA cert trusted in system store."
else
    echo "[setup] WARNING: CA cert not found at $CA_PATH"
fi

# Verify CA is in system store
if [ -f /etc/ssl/certs/agentsaegis.pem ]; then
    echo "[setup] Verified: CA in /etc/ssl/certs/agentsaegis.pem"
else
    echo "[setup] WARNING: CA not found in /etc/ssl/certs/ - MITM may not work"
fi

# 7. Set up shell wrappers + NODE_EXTRA_CA_CERTS for qa user
cat >> /home/qa/.bashrc <<BASHRC

# AgentsAegis shell wrappers
export NODE_EXTRA_CA_CERTS=/home/qa/.agentsaegis/ca.pem
claude() {
    ANTHROPIC_BASE_URL=http://localhost:7331 command claude "\$@"
}
copilot() {
    HTTPS_PROXY=http://localhost:7331 command copilot "\$@"
}
export -f claude copilot
BASHRC
chown qa:qa /home/qa/.bashrc

echo
echo "=== Ready! Run: claude or copilot ==="
echo

# 8. Drop to interactive shell as qa user
exec gosu qa bash --login
'
