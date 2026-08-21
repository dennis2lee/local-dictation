#!/usr/bin/env bash
#
# Build the Windows client executable and stage everything the MSI installs.
#
#   build/windows/build-exe.sh [--version 0.1.0] [--output dist] [--arch amd64]
#
# Runs on Windows (with a native gcc) or cross-compiles from macOS/Linux with
# mingw-w64. Fyne, miniaudio and the hotkey library all need cgo, so a
# CGO_ENABLED=0 build is not an option.
#
#   brew install mingw-w64          # macOS
#   apt-get install gcc-mingw-w64   # Debian/Ubuntu
#
# The staged tree is what the MSI packages: the .exe, the Python server source,
# and the docs. No model, no Python runtime, no wheels.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="0.1.0"
OUTPUT="$ROOT/dist"
ARCH="amd64"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:?}"; shift ;;
    --output)  OUTPUT="${2:?}"; shift ;;
    --arch)    ARCH="${2:?}"; shift ;;
    -h|--help) sed -n '3,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

STAGE="$OUTPUT/windows-$ARCH"
rm -rf "$STAGE"
mkdir -p "$STAGE/server" "$STAGE/docs"

# Pick a compiler. On Windows the toolchain is already correct; elsewhere we
# need the mingw cross-compiler that matches the target architecture.
if [[ "$(uname -s)" == MINGW* || "$(uname -s)" == MSYS* || "$(uname -s)" == CYGWIN* ]]; then
  export CC="${CC:-gcc}" CXX="${CXX:-g++}"
else
  case "$ARCH" in
    amd64) prefix="x86_64-w64-mingw32" ;;
    386)   prefix="i686-w64-mingw32" ;;
    *)     die "no known mingw prefix for GOARCH=$ARCH" ;;
  esac
  command -v "$prefix-gcc" >/dev/null || die "$prefix-gcc not found; install mingw-w64"
  export CC="$prefix-gcc" CXX="$prefix-g++"
fi

info "building the client for windows/$ARCH with $CC"
# -H=windowsgui suppresses the console window that would otherwise flash up
# behind a GUI application on every launch.
( cd "$ROOT/client" && \
  CGO_ENABLED=1 GOOS=windows GOARCH="$ARCH" \
  go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$VERSION" \
           -o "$STAGE/local-dictation.exe" ./cmd/local-dictation )

info "staging the Python server"
( cd "$ROOT/server" && \
  tar --exclude='__pycache__' --exclude='*.pyc' --exclude='.venv' --exclude='.pytest_cache' \
      -cf - app config scripts pyproject.toml ) | tar -xf - -C "$STAGE/server"

info "staging the documentation"
cp "$ROOT/docs/"*.md "$STAGE/docs/"
[[ -f "$ROOT/README.md" ]] && cp "$ROOT/README.md" "$STAGE/docs/"

info "rendering the icon"
python3 "$ROOT/build/assets/make-icon.py" "$STAGE/.icon" >/dev/null
# WiX wants a .ico; ImageMagick is optional, so fall back to the largest PNG and
# let the MSI use the default icon rather than failing the build.
if command -v magick >/dev/null 2>&1; then
  magick "$STAGE/.icon/icon-16.png" "$STAGE/.icon/icon-32.png" "$STAGE/.icon/icon-64.png" \
         "$STAGE/.icon/icon-128.png" "$STAGE/.icon/icon-256.png" "$STAGE/local-dictation.ico"
elif command -v convert >/dev/null 2>&1; then
  convert "$STAGE/.icon/icon-16.png" "$STAGE/.icon/icon-32.png" "$STAGE/.icon/icon-64.png" \
          "$STAGE/.icon/icon-128.png" "$STAGE/.icon/icon-256.png" "$STAGE/local-dictation.ico"
else
  info "no ImageMagick — writing the .ico by hand"
  python3 "$ROOT/build/assets/png-to-ico.py" "$STAGE/.icon" "$STAGE/local-dictation.ico"
fi
rm -rf "$STAGE/.icon"

info "staged $STAGE"
du -sh "$STAGE"
