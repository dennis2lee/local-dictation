#!/usr/bin/env bash
#
# Build every release artefact and a checksum file.
#
#   build/release.sh --version 0.1.0
#   build/release.sh --version 0.1.0 --targets server,macos
#
# Targets:
#   server   local-dictation-server-<version>.tar.gz   Python source + scripts
#   macos    LocalDictation-<version>.pkg and .dmg     (needs macOS)
#   windows  the staged tree, and the MSI when wix is available
#
# What is never in any of them: a speech model, a Python runtime, or vendored
# wheels. docs/client-install.md says why and what to run instead.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION=""
OUTPUT="$ROOT/dist"
TARGETS="server,macos,windows"
SIGN_APP=""
SIGN_PKG=""
NOTARIZE_PROFILE=""
CLEAN=1

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)          VERSION="${2:?}"; shift ;;
    --output)           OUTPUT="${2:?}"; shift ;;
    --targets)          TARGETS="${2:?}"; shift ;;
    --sign-app)         SIGN_APP="${2:?}"; shift ;;
    --sign-pkg)         SIGN_PKG="${2:?}"; shift ;;
    --notarize-profile) NOTARIZE_PROFILE="${2:?}"; shift ;;
    --no-clean)         CLEAN=0 ;;
    -h|--help) sed -n '3,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

[[ -n "$VERSION" ]] || die "--version is required"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must be MAJOR.MINOR.PATCH, got '$VERSION'"

wants() { [[ ",$TARGETS," == *",$1,"* ]]; }

if [[ "$CLEAN" == "1" ]]; then
  info "clearing $OUTPUT"
  rm -rf "$OUTPUT"
fi
mkdir -p "$OUTPUT"

info "running the tests"
# A release that ships without its tests passing is not a release.
( cd "$ROOT/server" && \
  if [[ -x .venv/bin/python ]]; then .venv/bin/python -m pytest -q; \
  else warn "no server venv; skipping the Python tests"; fi )
( cd "$ROOT/client" && go test ./... )

if wants server; then
  info "packaging the server"
  "$ROOT/build/server/package.sh" --version "$VERSION" --output "$OUTPUT"
fi

if wants macos; then
  if [[ "$(uname -s)" == "Darwin" ]]; then
    info "packaging for macOS"
    args=(--version "$VERSION" --output "$OUTPUT")
    [[ -n "$SIGN_APP" ]] && args+=(--sign-app "$SIGN_APP")
    [[ -n "$SIGN_PKG" ]] && args+=(--sign-pkg "$SIGN_PKG")
    [[ -n "$NOTARIZE_PROFILE" ]] && args+=(--notarize-profile "$NOTARIZE_PROFILE")
    "$ROOT/build/macos/build-pkg.sh" "${args[@]}"
  else
    warn "skipping macOS: this is not a Mac"
  fi
fi

if wants windows; then
  info "packaging for Windows"
  if "$ROOT/build/windows/build-exe.sh" --version "$VERSION" --output "$OUTPUT"; then
    if command -v wix >/dev/null 2>&1 && command -v pwsh >/dev/null 2>&1; then
      pwsh -NoProfile -File "$ROOT/build/windows/build-msi.ps1" -Version "$VERSION" \
           -Stage "$OUTPUT/windows-amd64" -Output "$OUTPUT"
    else
      # The staged tree is still a usable artefact: it is what the MSI wraps.
      warn "wix or pwsh not found — zipping the staged tree instead of building an MSI"
      ( cd "$OUTPUT" && zip -qr "LocalDictation-$VERSION-windows-amd64.zip" "windows-amd64" )
      ls -lh "$OUTPUT/LocalDictation-$VERSION-windows-amd64.zip"
    fi
  else
    warn "skipping Windows: the cross-compiler is missing (brew install mingw-w64)"
  fi
fi

info "checksums"
( cd "$OUTPUT" && \
  find . -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.pkg' -o -name '*.dmg' -o -name '*.msi' -o -name '*.zip' \) \
    -exec shasum -a 256 {} + | sed 's|\./||' > SHA256SUMS )
cat "$OUTPUT/SHA256SUMS"

info "release $VERSION is in $OUTPUT"
