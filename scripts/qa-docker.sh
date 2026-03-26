#!/usr/bin/env bash
# qa-docker.sh - Host-side orchestration for Docker QA
set -euo pipefail

# Defaults
TARGET="all"
BUILD_ONLY=false
NO_CACHE=""

usage() {
    echo "Usage: $0 [--claude|--copilot|--all] [--build-only] [--no-cache]"
    echo
    echo "  --claude      Run Claude Code QA tests only"
    echo "  --copilot     Run Copilot CLI QA tests only"
    echo "  --all         Run all QA tests (default)"
    echo "  --build-only  Build image only, do not run tests"
    echo "  --no-cache    Build Docker image without cache"
    exit 1
}

# Parse args
while [ $# -gt 0 ]; do
    case "$1" in
        --claude)   TARGET="claude" ;;
        --copilot)  TARGET="copilot" ;;
        --all)      TARGET="all" ;;
        --build-only) BUILD_ONLY=true ;;
        --no-cache) NO_CACHE="--no-cache" ;;
        -h|--help)  usage ;;
        *) echo "Unknown option: $1"; usage ;;
    esac
    shift
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== AgentsAegis Docker QA ==="
echo "Target: $TARGET"
echo

# Auth detection (warn but don't block - container scripts handle hard failures)
echo "--- Auth detection ---"
CLAUDE_AUTH_OK=false
COPILOT_AUTH_OK=false

if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    echo "[OK]  ANTHROPIC_API_KEY is set (Claude Code auth)"
    CLAUDE_AUTH_OK=true
else
    echo "[WARN] ANTHROPIC_API_KEY is not set - Claude tests will fail"
    echo "       Set it with: export ANTHROPIC_API_KEY=sk-ant-..."
fi

if [ -d "$HOME/.copilot" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
    if [ -d "$HOME/.copilot" ]; then
        echo "[OK]  ~/.copilot/ found (Copilot session state)"
    fi
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        echo "[OK]  GITHUB_TOKEN is set (Copilot auth)"
    fi
    COPILOT_AUTH_OK=true
else
    echo "[WARN] No Copilot auth found - Copilot tests will be skipped"
    echo "       Mount ~/.copilot/ from host or set GITHUB_TOKEN"
fi

if [ -f "$HOME/.config/gh/hosts.yml" ]; then
    echo "[OK]  ~/.config/gh/hosts.yml found (GitHub CLI auth)"
    COPILOT_AUTH_OK=true
fi
echo

# Build Docker image
echo "--- Building Docker image ---"
docker build $NO_CACHE -f "$REPO_ROOT/Dockerfile.qa" -t agentsaegis-qa:local "$REPO_ROOT"
echo "[OK] Image built: agentsaegis-qa:local"
echo

if [ "$BUILD_ONLY" = "true" ]; then
    echo "Build-only mode - exiting without running tests."
    exit 0
fi

# Assemble volume mounts
MOUNTS=()
if [ -d "$HOME/.copilot" ]; then
    MOUNTS+=("-v" "$HOME/.copilot:/home/qa/.copilot:ro")
fi
if [ -d "$HOME/.config/gh" ]; then
    MOUNTS+=("-v" "$HOME/.config/gh:/home/qa/.config/gh:ro")
fi

# Run container
echo "--- Running QA container ---"
EXIT_CODE=0
docker run --rm \
    -e "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}" \
    -e "GITHUB_TOKEN=${GITHUB_TOKEN:-}" \
    -e "TEST_TARGET=$TARGET" \
    "${MOUNTS[@]}" \
    agentsaegis-qa:local || EXIT_CODE=$?

echo
if [ "$EXIT_CODE" -eq 0 ]; then
    echo "Docker QA: PASS"
else
    echo "Docker QA: FAIL (exit $EXIT_CODE)"
fi

exit "$EXIT_CODE"
