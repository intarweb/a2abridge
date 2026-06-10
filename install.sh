#!/usr/bin/env bash
# install.sh — one-line installer for macOS / Linux / WSL2.
#
# Typical usage:
#   curl -fsSL https://raw.githubusercontent.com/<owner>/a2abridge/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/<owner>/a2abridge/main/install.sh | bash -s -- --version v2.1.0
#
# Env overrides:
#   A2A_REPO         GitHub repo in owner/name form (default: vbcherepanov/a2abridge)
#   A2A_VERSION      tag to install (default: latest release)
#   A2A_PREFIX       install prefix (default: ~/.a2abridge)
#   A2A_NO_SERVICE   set to "1" to skip the supervisor install step
#   A2A_NO_IDE       set to "1" to skip writing IDE configs

set -euo pipefail

usage() {
  cat <<'EOF'
install.sh — one-line installer for a2abridge (macOS / Linux / WSL2).

Usage:
  install.sh [--version vX.Y.Z] [--prefix DIR] [--dry-run]

Flags:
  --version vX.Y.Z   install a specific release tag (default: latest)
  --prefix DIR       install prefix (default: ~/.a2abridge)
  --dry-run          download + verify only; skip IDE/service registration
  --help, -h         show this help

Env overrides:
  A2A_REPO         GitHub repo in owner/name form (default: vbcherepanov/a2abridge)
  A2A_VERSION      tag to install (default: latest release)
  A2A_PREFIX       install prefix (default: ~/.a2abridge)
  A2A_NO_SERVICE   set to "1" to skip the supervisor install step
  A2A_NO_IDE       set to "1" to skip writing IDE configs
EOF
}

# sha256 <file> — portable digest (sha256sum on Linux, shasum on macOS).
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "" # caller treats empty as fatal
  fi
}

# The whole installer lives in main() so a truncated `curl | bash` can
# never execute half a script: bash only runs main after parsing the
# entire file.
main() {
  REPO="${A2A_REPO:-vbcherepanov/a2abridge}"
  VERSION="${A2A_VERSION:-}"
  PREFIX="${A2A_PREFIX:-$HOME/.a2abridge}"
  APPLY="--apply"

  while [ $# -gt 0 ]; do
    case "$1" in
      --version) VERSION="$2"; shift 2 ;;
      --prefix)  PREFIX="$2";  shift 2 ;;
      --dry-run) APPLY="";     shift ;;
      --help|-h) usage; exit 0 ;;
      *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
  done

  # --- detect platform ---------------------------------------------------
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
  esac
  case "$os" in
    darwin|linux) ;;
    *) echo "unsupported OS for install.sh: $os (use install.ps1 on Windows)" >&2; exit 1 ;;
  esac

  # --- resolve version ---------------------------------------------------
  if [ -z "$VERSION" ]; then
    echo "→ resolving latest release for $REPO"
    VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
      | grep -m1 '"tag_name"' \
      | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
  fi
  [ -z "$VERSION" ] && { echo "could not resolve latest version" >&2; exit 1; }
  echo "→ installing a2abridge $VERSION ($os/$arch) into $PREFIX"

  # --- download ------------------------------------------------------------
  TARBALL="a2abridge_${VERSION#v}_${os}_${arch}.tar.gz"
  URL="https://github.com/$REPO/releases/download/$VERSION/$TARBALL"
  SUMS_URL="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"
  TMP=$(mktemp -d -t a2abridge-install-XXXXXX)
  trap 'rm -rf "$TMP"' EXIT

  echo "→ downloading $URL"
  if ! curl -fsSL "$URL" -o "$TMP/$TARBALL"; then
    echo "download failed; verify a release exists at $URL" >&2
    exit 1
  fi

  # --- verify checksum (mandatory) ----------------------------------------
  echo "→ verifying sha256 against checksums.txt"
  if ! curl -fsSL "$SUMS_URL" -o "$TMP/checksums.txt"; then
    echo "checksums.txt is missing from release $VERSION — this installer refuses unverified binaries." >&2
    echo "Older tags were published without checksums; pin a release that ships checksums.txt:" >&2
    echo "  install.sh --version vX.Y.Z" >&2
    exit 1
  fi
  expected=$(awk -v f="$TARBALL" '$2 == f {print $1}' "$TMP/checksums.txt")
  if [ -z "$expected" ]; then
    echo "checksums.txt in release $VERSION has no entry for $TARBALL" >&2
    exit 1
  fi
  actual=$(sha256 "$TMP/$TARBALL")
  if [ -z "$actual" ]; then
    echo "neither sha256sum nor shasum found on PATH; cannot verify download" >&2
    exit 1
  fi
  if [ "$expected" != "$actual" ]; then
    echo "CHECKSUM MISMATCH for $TARBALL" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    echo "The download is corrupted or tampered with. Aborting." >&2
    exit 1
  fi
  echo "  sha256 OK: $actual"

  # --- extract -------------------------------------------------------------
  mkdir -p "$PREFIX/bin"
  tar -xzf "$TMP/$TARBALL" -C "$TMP"
  # Tarballs ship a single binary at the root.
  mv "$TMP/a2abridge" "$PREFIX/bin/a2abridge"
  chmod +x "$PREFIX/bin/a2abridge"

  # --- register IDEs + skill + hook --------------------------------------
  if [ "${A2A_NO_IDE:-0}" != "1" ]; then
    echo "→ registering MCP server in detected IDEs"
    "$PREFIX/bin/a2abridge" install $APPLY
  fi

  # --- service supervisor ------------------------------------------------
  if [ -n "$APPLY" ] && [ "${A2A_NO_SERVICE:-0}" != "1" ]; then
    echo "→ installing directory daemon"
    "$PREFIX/bin/a2abridge" service install || \
      echo "  service install failed — you can retry with: $PREFIX/bin/a2abridge service install"
  fi

  # --- post-install summary ---------------------------------------------
  cat <<EOF

a2abridge $VERSION installed.

  binary:  $PREFIX/bin/a2abridge
  doctor:  $PREFIX/bin/a2abridge doctor
  service: $PREFIX/bin/a2abridge service status

Add this to your shell profile so a2abridge is on PATH:

  export PATH="$PREFIX/bin:\$PATH"

Restart your IDEs (Claude Code, Codex, Cursor, ...) to pick up the new MCP server.
EOF
}

main "$@"
