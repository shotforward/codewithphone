#!/usr/bin/env bash
set -euo pipefail

REPO="${CODEWITHPHONE_REPO:-shotforward/codewithphone}"
TAG="${1:-${CODEWITHPHONE_VERSION:-}}"

detect_os() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)
      echo "unsupported OS: $(uname -s) (supported: linux, darwin)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "unsupported architecture: $(uname -m) (supported: amd64, arm64)" >&2
      exit 1
      ;;
  esac
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "required command not found: $1" >&2
    exit 1
  fi
}

latest_tag() {
  require_cmd curl
  local api="https://api.github.com/repos/${REPO}/releases/latest"
  local latest
  latest="$(curl -fsSL "$api" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  if [[ -z "${latest}" ]]; then
    echo "failed to discover latest release tag from ${api}" >&2
    exit 1
  fi
  echo "${latest}"
}

verify_checksum() {
  local file="$1"
  local checksums_file="$2"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$(dirname "$file")" && sha256sum -c "$checksums_file" --ignore-missing)
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    local expected
    expected="$(grep "  $(basename "$file")\$" "$checksums_file" | awk '{print $1}')"
    if [[ -z "${expected}" ]]; then
      echo "checksum not found for $(basename "$file")" >&2
      exit 1
    fi
    local actual
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
    if [[ "${expected}" != "${actual}" ]]; then
      echo "checksum mismatch for $(basename "$file")" >&2
      echo "expected: ${expected}" >&2
      echo "actual:   ${actual}" >&2
      exit 1
    fi
    return 0
  fi
  echo "warning: sha256sum/shasum not found, skipping checksum verification" >&2
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
if [[ -z "${TAG}" ]]; then
  TAG="$(latest_tag)"
fi

ASSET="codewithphone_${TAG}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

echo "Downloading ${ASSET} (${TAG})..."
curl -fsSL "${BASE_URL}/${ASSET}" -o "${TMP_DIR}/${ASSET}"
curl -fsSL "${BASE_URL}/${CHECKSUMS}" -o "${TMP_DIR}/${CHECKSUMS}"

verify_checksum "${TMP_DIR}/${ASSET}" "${TMP_DIR}/${CHECKSUMS}"

tar -xzf "${TMP_DIR}/${ASSET}" -C "${TMP_DIR}"
if [[ ! -f "${TMP_DIR}/codewithphone" ]]; then
  echo "archive does not contain codewithphone binary" >&2
  exit 1
fi

TARGET_BIN="/usr/local/bin/codewithphone"
if [[ -w "/usr/local/bin" ]]; then
  install -m 0755 "${TMP_DIR}/codewithphone" "${TARGET_BIN}"
  echo "Installed to ${TARGET_BIN}"
elif command -v sudo >/dev/null 2>&1; then
  sudo install -m 0755 "${TMP_DIR}/codewithphone" "${TARGET_BIN}"
  echo "Installed to ${TARGET_BIN} (via sudo)"
else
  USER_BIN_DIR="${HOME}/.local/bin"
  mkdir -p "${USER_BIN_DIR}"
  install -m 0755 "${TMP_DIR}/codewithphone" "${USER_BIN_DIR}/codewithphone"
  echo "Installed to ${USER_BIN_DIR}/codewithphone"
  echo "Add this to your shell profile if needed:"
  echo "  export PATH=\"${USER_BIN_DIR}:\$PATH\""
fi

echo
codewithphone version
