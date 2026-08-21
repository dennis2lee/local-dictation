#!/usr/bin/env bash
#
# Package the server for release.
#
#   build/server/package.sh [--version 0.1.0] [--output dist]
#
# The result is a tarball of Python source, configs, scripts and an installer.
# That is the whole server: no compiled artefacts, no vendored dependencies, no
# model. Ubuntu or macOS, any Python 3.11+.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="0.1.0"
OUTPUT="$ROOT/dist"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:?}"; shift ;;
    --output)  OUTPUT="${2:?}"; shift ;;
    -h|--help) sed -n '3,11p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

# The VERSION file in the tarball and the version the server reports at /health
# have to be the same number, or `local-dictation-server version` shows a
# mismatch that no restart can fix. The code is the source of truth, so this is
# also what catches a release tag that ran ahead of a forgotten version bump.
check_version_matches() {
  local file="$1" found="$2"
  [[ -n "$found" ]] || die "could not read a version from $file"
  [[ "$found" == "$VERSION" ]] || die \
    "--version $VERSION does not match $file ($found); bump the code, or tag the version it already has"
}
check_version_matches "server/app/__init__.py" \
  "$(sed -n 's/^__version__ = "\(.*\)"/\1/p' "$ROOT/server/app/__init__.py")"
check_version_matches "server/pyproject.toml" \
  "$(sed -n 's/^version = "\(.*\)"/\1/p' "$ROOT/server/pyproject.toml")"

NAME="local-dictation-server-$VERSION"
STAGE="$OUTPUT/.$NAME"
TARBALL="$OUTPUT/$NAME.tar.gz"

rm -rf "$STAGE"
mkdir -p "$STAGE" "$OUTPUT"

info "staging the server"
( cd "$ROOT/server" && \
  tar --exclude='__pycache__' --exclude='*.pyc' --exclude='.venv' --exclude='.pytest_cache' \
      -cf - app config scripts tests pyproject.toml ) | tar -xf - -C "$STAGE"

mkdir -p "$STAGE/protocol"
cp -R "$ROOT/protocol/." "$STAGE/protocol/"
mkdir -p "$STAGE/docs"
cp "$ROOT/docs/"*.md "$STAGE/docs/"
[[ -f "$ROOT/LICENSE" ]] && cp "$ROOT/LICENSE" "$STAGE/"

printf '%s\n' "$VERSION" > "$STAGE/VERSION"

info "writing the installer"
cp "$ROOT/build/server/install.sh" "$STAGE/install.sh"
chmod +x "$STAGE/install.sh" "$STAGE/scripts/"*.sh "$STAGE/scripts/local-dictation-server"

info "building $TARBALL"
rm -f "$TARBALL"
( cd "$OUTPUT" && tar czf "$(basename "$TARBALL")" --strip-components=0 -C "$OUTPUT" ".$NAME" \
    --transform "s|^\.$NAME|$NAME|" 2>/dev/null ) || \
( cd "$OUTPUT" && mv ".$NAME" "$NAME" && tar czf "$(basename "$TARBALL")" "$NAME" && mv "$NAME" ".$NAME" )

rm -rf "$STAGE"

info "built:"
ls -lh "$TARBALL" | sed 's/^/    /'
shasum -a 256 "$TARBALL" | sed 's/^/    /'
