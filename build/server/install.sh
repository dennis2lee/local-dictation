#!/usr/bin/env bash
#
# Install the Local Dictation server.
#
#   sudo ./install.sh                      # into /opt/local-dictation
#   ./install.sh --prefix ~/local-dictation --user
#
# Creates the directory layout, builds a virtual environment, installs the
# Python dependencies, and puts the management command on PATH. It does not
# register a service: `local-dictation-server start all` runs both language
# servers as background processes, which is all this needs.
#
# It does not download a model either. That is one more command, and it is in
# docs/model-setup.md.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFIX="/opt/local-dictation"
PYTHON=""
LINK_DIR=""
USER_MODE=0

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)  PREFIX="${2:?}"; shift ;;
    --python)  PYTHON="${2:?}"; shift ;;
    --link)    LINK_DIR="${2:?}"; shift ;;
    --user)    USER_MODE=1; PREFIX="${PREFIX/\/opt\/local-dictation/$HOME/local-dictation}" ;;
    -h|--help) sed -n '3,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

if [[ "$USER_MODE" == "0" && "$PREFIX" == /opt/* && "$(id -u)" != "0" ]]; then
  die "installing into $PREFIX needs root. Re-run with sudo, or use --user."
fi

# Find an interpreter new enough for the server.
if [[ -z "$PYTHON" ]]; then
  for candidate in python3.13 python3.12 python3.11 python3; do
    if command -v "$candidate" >/dev/null 2>&1; then
      version="$("$candidate" -c 'import sys; print("%d%02d" % sys.version_info[:2])')"
      if [[ "$version" -ge 311 ]]; then PYTHON="$(command -v "$candidate")"; break; fi
    fi
  done
fi
[[ -n "$PYTHON" ]] || die "no Python 3.11 or newer found. Install one, or pass --python."
info "using $PYTHON ($("$PYTHON" -c 'import sys; print("%d.%d" % sys.version_info[:2])'))"

info "installing into $PREFIX"
mkdir -p "$PREFIX"/{app,config,models,run,log,tls}

# app/ holds the Python package; the management script expects `app` directly
# under LD_APP_DIR.
cp -R "$HERE/app" "$PREFIX/app/"
cp -R "$HERE/scripts" "$PREFIX/app/"
cp "$HERE/pyproject.toml" "$PREFIX/app/"
[[ -d "$HERE/protocol" ]] && cp -R "$HERE/protocol" "$PREFIX/app/"
[[ -d "$HERE/docs" ]] && cp -R "$HERE/docs" "$PREFIX/"
[[ -f "$HERE/VERSION" ]] && cp "$HERE/VERSION" "$PREFIX/"

# Never overwrite a config an operator has edited.
for language in ko en; do
  target="$PREFIX/config/server-$language.yaml"
  if [[ -f "$target" ]]; then
    info "keeping the existing $target"
  else
    cp "$HERE/config/server-$language.yaml" "$target"
  fi
done

info "creating the virtual environment"
"$PYTHON" -m venv "$PREFIX/venv"
"$PREFIX/venv/bin/python" -m pip install --quiet --upgrade pip
info "installing dependencies (this pulls ctranslate2, which takes a minute)"
"$PREFIX/venv/bin/python" -m pip install --quiet \
  "fastapi>=0.115" "uvicorn[standard]>=0.30" "pyyaml>=6.0" "numpy>=1.26" \
  "faster-whisper>=1.0.3" "onnxruntime>=1.18"

# The management script reads its paths from the environment, so a wrapper is
# how a system install gets sensible ones without editing the script.
info "installing the management command"
cat > "$PREFIX/bin-local-dictation-server" <<WRAPPER
#!/usr/bin/env bash
export LD_HOME="$PREFIX"
export LD_APP_DIR="$PREFIX/app"
export LD_CONFIG_DIR="$PREFIX/config"
export LD_RUN_DIR="$PREFIX/run"
export LD_LOG_DIR="$PREFIX/log"
export LD_PYTHON="$PREFIX/venv/bin/python"
exec "$PREFIX/app/scripts/local-dictation-server" "\$@"
WRAPPER
chmod +x "$PREFIX/bin-local-dictation-server" "$PREFIX/app/scripts/"*

if [[ -z "$LINK_DIR" ]]; then
  LINK_DIR="/usr/local/bin"
  [[ "$USER_MODE" == "1" ]] && LINK_DIR="$HOME/.local/bin"
fi
if mkdir -p "$LINK_DIR" 2>/dev/null && ln -sf "$PREFIX/bin-local-dictation-server" "$LINK_DIR/local-dictation-server" 2>/dev/null; then
  info "linked $LINK_DIR/local-dictation-server"
else
  info "could not link into $LINK_DIR; run $PREFIX/bin-local-dictation-server directly"
fi

cat <<NEXT

Installed under $PREFIX.

Next, two commands:

  1. Install a model (2.9 GB for large-v3, 1.5 GB for the faster large-v3-turbo):

       $PREFIX/app/scripts/fetch-model.sh large-v3-turbo --dest $PREFIX/models

  2. Point both configs at it, then start:

       \$EDITOR $PREFIX/config/server-ko.yaml   # model.path and streaming.silero_model_path
       \$EDITOR $PREFIX/config/server-en.yaml
       local-dictation-server start all
       local-dictation-server health all

TLS is on by default and expects certificates under $PREFIX/tls. For a first
run on a trusted network, set security.tls_certificate and tls_private_key to
null in both configs and turn off require_client_certificate.

Full instructions: $PREFIX/docs/installation.md
NEXT
