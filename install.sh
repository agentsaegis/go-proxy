#!/bin/sh
set -eu

# AgentsAegis installer
# Usage: curl -sSL https://raw.githubusercontent.com/agentsaegis/go-proxy/main/install.sh | sh
#
# Environment variables:
#   VERSION      - Pin to a specific version (e.g. VERSION=v0.3.0)
#   INSTALL_DIR  - Override install directory (default: /usr/local/bin or ~/.local/bin)
#   GITHUB_TOKEN - GitHub token for API requests (avoids rate limits)

REPO="agentsaegis/go-proxy"
BINARY="agentsaegis"

# --- Detect OS ---

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    darwin) ;;
    linux) ;;
    *)
        printf "Error: unsupported OS: %s\n" "$OS" >&2
        exit 1
        ;;
esac

# --- Detect architecture ---

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
        printf "Error: unsupported architecture: %s\n" "$ARCH" >&2
        exit 1
        ;;
esac

# --- Resolve version ---

if [ -n "${VERSION:-}" ]; then
    TAG="$VERSION"
else
    printf "Fetching latest release...\n"
    AUTH_HEADER=""
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        AUTH_HEADER="Authorization: token ${GITHUB_TOKEN}"
    fi

    HTTP_RESPONSE=$(mktemp)
    HTTP_CODE=$(curl -sSL -o "$HTTP_RESPONSE" -w "%{http_code}" \
        ${AUTH_HEADER:+-H "$AUTH_HEADER"} \
        "https://api.github.com/repos/${REPO}/releases/latest")

    if [ "$HTTP_CODE" != "200" ]; then
        printf "Error: failed to fetch latest release (HTTP %s)\n" "$HTTP_CODE" >&2
        if [ "$HTTP_CODE" = "403" ]; then
            printf "This may be due to GitHub API rate limiting.\n" >&2
            printf "Set GITHUB_TOKEN to avoid rate limits.\n" >&2
        fi
        rm -f "$HTTP_RESPONSE"
        exit 1
    fi

    TAG=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$HTTP_RESPONSE")
    rm -f "$HTTP_RESPONSE"

    if [ -z "$TAG" ]; then
        printf "Error: could not determine latest version\n" >&2
        exit 1
    fi
fi

# Goreleaser strips the v prefix from archive names
VERSION_NUM="${TAG#v}"

printf "Installing %s %s for %s/%s...\n" "$BINARY" "$TAG" "$OS" "$ARCH"

# --- Download and verify ---

ARCHIVE="${BINARY}_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

curl -sSL -f -o "${TMPDIR}/${ARCHIVE}" "${BASE_URL}/${ARCHIVE}" || {
    printf "Error: failed to download %s\n" "${BASE_URL}/${ARCHIVE}" >&2
    exit 1
}

curl -sSL -f -o "${TMPDIR}/checksums.txt" "${BASE_URL}/checksums.txt" || {
    printf "Error: failed to download checksums\n" >&2
    exit 1
}

# Verify checksum
EXPECTED=$(grep "${ARCHIVE}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
if [ -z "$EXPECTED" ]; then
    printf "Error: archive not found in checksums.txt\n" >&2
    exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL=$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    ACTUAL=$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
else
    printf "Warning: no sha256sum or shasum found, skipping checksum verification\n" >&2
    ACTUAL="$EXPECTED"
fi

if [ "$ACTUAL" != "$EXPECTED" ]; then
    printf "Error: checksum verification failed\n" >&2
    printf "  expected: %s\n" "$EXPECTED" >&2
    printf "  actual:   %s\n" "$ACTUAL" >&2
    exit 1
fi

# --- Extract and install ---

tar -xzf "${TMPDIR}/${ARCHIVE}" -C "${TMPDIR}"

# Determine install directory
if [ -n "${INSTALL_DIR:-}" ]; then
    TARGET_DIR="$INSTALL_DIR"
elif [ -w "/usr/local/bin" ]; then
    TARGET_DIR="/usr/local/bin"
else
    TARGET_DIR="${HOME}/.local/bin"
fi

mkdir -p "$TARGET_DIR"
mv "${TMPDIR}/${BINARY}" "${TARGET_DIR}/${BINARY}"
chmod +x "${TARGET_DIR}/${BINARY}"

printf "\n%s %s installed to %s/%s\n" "$BINARY" "$TAG" "$TARGET_DIR" "$BINARY"

# Warn if not in PATH
case ":${PATH}:" in
    *":${TARGET_DIR}:"*) ;;
    *)
        printf "\nNote: %s is not in your PATH.\n" "$TARGET_DIR"
        printf "Add it with:\n"
        printf "  export PATH=\"%s:\$PATH\"\n" "$TARGET_DIR"
        ;;
esac

printf "\nGet started:\n"
printf "  agentsaegis init\n"
printf "  agentsaegis start --daemon\n"
printf "  agentsaegis setup-shell\n"
