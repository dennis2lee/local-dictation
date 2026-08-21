#!/usr/bin/env bash
#
# Install the Local Dictation server.
#
#   ./install.sh                           # into ~/local-dictation, no sudo
#   sudo ./install.sh                      # into /opt/local-dictation
#   ./install.sh --prefix /srv/dictation   # anywhere you can write
#
# Running it without sudo is the normal case and needs no privileges at all: the
# prefix, the config paths inside it and the command on PATH all move together.
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
PREFIX=""
PYTHON=""
LINK_DIR=""
USER_MODE=0

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

# Derived, not hard-coded: a line added to the header above must not silently
# truncate the help.
usage() {
  local last
  last=$(( $(grep -n '^set -euo pipefail' "$0" | head -1 | cut -d: -f1) - 2 ))
  sed -n "3,${last}p" "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)  PREFIX="${2:?}"; shift ;;
    --python)  PYTHON="${2:?}"; shift ;;
    --link)    LINK_DIR="${2:?}"; shift ;;
    --user)    USER_MODE=1 ;;
    -h|--help) usage ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

if [[ "$USER_MODE" == "1" && -n "${SUDO_USER:-}" ]]; then
  # Under sudo, $HOME is root's, so a --user install would land in /var/root and
  # be no more usable than the /opt one. Say so instead of guessing.
  die "--user installs for whoever runs the command. Re-run it without sudo."
fi

# Choose the prefix only after every flag has been read, so --user and --prefix
# do not depend on the order they were written in. Not being root is not an
# error: it selects a prefix you own, which is the install that then needs no
# privileges to operate.
if [[ -z "$PREFIX" ]]; then
  if [[ "$USER_MODE" == "1" || "$(id -u)" != "0" ]]; then
    PREFIX="$HOME/local-dictation"
  else
    PREFIX="/opt/local-dictation"
  fi
fi
[[ "$(id -u)" == "0" ]] || USER_MODE=1

# Everything downstream — the wrapper, the rewritten configs — embeds this path,
# so resolve a relative one now rather than baking in a working directory.
parent="$(dirname "$PREFIX")"
if [[ -d "$PREFIX" ]]; then
  [[ -w "$PREFIX" ]] || die "$PREFIX exists but you cannot write to it. Re-run with sudo, or pass --prefix \$HOME/local-dictation."
elif [[ ! -d "$parent" || ! -w "$parent" ]]; then
  die "cannot create $PREFIX: $parent is not writable. Re-run with sudo, or pass --prefix \$HOME/local-dictation."
fi
mkdir -p "$PREFIX"
PREFIX="$(cd "$PREFIX" && pwd)"

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

# Everyday commands are documented without sudo, and three of them write:
# `start` needs a pid file, it needs a log, and fetch-model.sh needs somewhere to
# put a model. A root install would make all three impossible — a start that
# cannot open its log fails as a redirection error inside a background job,
# which is about as unhelpful as a failure gets. So the directories the server
# writes to belong to whoever ran the install. config/ and app/ stay root-owned:
# those are policy and code, and editing them should take a deliberate sudo.
RUNTIME_OWNER="${SUDO_USER:-}"
if [[ -n "$RUNTIME_OWNER" ]]; then
  chown -R "$RUNTIME_OWNER" "$PREFIX"/{run,log,models}
  info "run/, log/ and models/ belong to $RUNTIME_OWNER — start, stop and fetch-model need no sudo"
fi

# app/ holds the Python package; the management script expects `app` directly
# under LD_APP_DIR.
cp -R "$HERE/app" "$PREFIX/app/"
cp -R "$HERE/scripts" "$PREFIX/app/"
cp "$HERE/pyproject.toml" "$PREFIX/app/"
[[ -d "$HERE/protocol" ]] && cp -R "$HERE/protocol" "$PREFIX/app/"
[[ -d "$HERE/docs" ]] && cp -R "$HERE/docs" "$PREFIX/"
[[ -f "$HERE/VERSION" ]] && cp "$HERE/VERSION" "$PREFIX/"

# The shipped configs name /opt/local-dictation in five places: the model, the
# VAD model and three TLS files. Copying them verbatim into another prefix is
# what made --prefix look supported while producing a server that could not find
# its own model, so the paths move with the install.
#
# A prefix you own also gets the posture that goes with it — loopback, no TLS.
# The 0.0.0.0-with-certificates default belongs to a shared server, and a
# listener open to the network with TLS switched off is the one combination
# nobody should arrive at by accident.
for language in ko en; do
  target="$PREFIX/config/server-$language.yaml"
  # Never overwrite a config an operator has edited.
  if [[ -f "$target" ]]; then
    info "keeping the existing $target"
    continue
  fi
  sed "s|/opt/local-dictation|$PREFIX|g" "$HERE/config/server-$language.yaml" > "$target"
  if [[ "$USER_MODE" == "1" ]]; then
    sed -i.bak \
      -e 's|^  host: "0.0.0.0"|  host: "127.0.0.1"|' \
      -e 's|^  tls_certificate: .*|  tls_certificate: null|' \
      -e 's|^  tls_private_key: .*|  tls_private_key: null|' \
      -e 's|^  client_ca: .*|  client_ca: null|' \
      -e 's|^  require_client_certificate: true|  require_client_certificate: false|' \
      "$target"
    rm -f "$target.bak"
  fi
done

info "creating the virtual environment"
"$PYTHON" -m venv "$PREFIX/venv"
"$PREFIX/venv/bin/python" -m pip install --quiet --upgrade pip
info "installing dependencies (this pulls ctranslate2, which takes a minute)"
"$PREFIX/venv/bin/python" -m pip install --quiet \
  "fastapi>=0.115" "uvicorn[standard]>=0.30" "pyyaml>=6.0" "numpy>=1.26" \
  "faster-whisper>=1.0.3" "onnxruntime>=1.18"

# pip exiting zero is not the same as the server being able to start. Import
# what it actually imports, so a partial install is caught here rather than as a
# ModuleNotFoundError traceback out of app/main.py at the operator's first
# command. onnxruntime is reported but not required: without it the server falls
# back to the energy voice detector rather than refusing to serve.
info "verifying the dependencies"
"$PREFIX/venv/bin/python" - <<'VERIFY' || die "the dependencies did not install completely; re-run this installer"
import importlib.util

required = ["fastapi", "uvicorn", "yaml", "numpy", "faster_whisper", "ctranslate2"]
missing = [m for m in required if importlib.util.find_spec(m) is None]
if missing:
    raise SystemExit("cannot import: " + ", ".join(missing))
if importlib.util.find_spec("onnxruntime") is None:
    print("    onnxruntime is absent; the server will use the energy voice detector")
VERIFY

# The configs above were produced by sed. Load them through the same code the
# server uses, so a rewrite that broke one is an install failure rather than a
# surprise at the first start.
info "validating the generated configuration"
for language in ko en; do
  ( cd "$PREFIX/app" && "$PREFIX/venv/bin/python" -c '
import sys
from app.settings import ConfigError, load_settings
try:
    load_settings(sys.argv[1])
except ConfigError as exc:
    raise SystemExit(f"{sys.argv[1]}: {exc}")
' "$PREFIX/config/server-$language.yaml" ) || die "the generated $language config is not valid"
done

# The management script reads its paths from the environment, so a wrapper is
# how a system install gets sensible ones without editing the script.
info "installing the management command"
cat > "$PREFIX/bin-local-dictation-server" <<WRAPPER
#!/usr/bin/env bash
# Defaults, not overrides: the script documents these as coming from the
# environment, and a wrapper that pinned them would quietly make that untrue.
export LD_HOME="\${LD_HOME:-$PREFIX}"
export LD_APP_DIR="\${LD_APP_DIR:-$PREFIX/app}"
export LD_CONFIG_DIR="\${LD_CONFIG_DIR:-$PREFIX/config}"
export LD_RUN_DIR="\${LD_RUN_DIR:-$PREFIX/run}"
export LD_LOG_DIR="\${LD_LOG_DIR:-$PREFIX/log}"
export LD_PYTHON="\${LD_PYTHON:-$PREFIX/venv/bin/python}"
exec "$PREFIX/app/scripts/local-dictation-server" "\$@"
WRAPPER
chmod +x "$PREFIX/bin-local-dictation-server" "$PREFIX/app/scripts/"*

if [[ -z "$LINK_DIR" ]]; then
  LINK_DIR="/usr/local/bin"
  [[ "$USER_MODE" == "1" ]] && LINK_DIR="$HOME/.local/bin"
fi
COMMAND="$PREFIX/bin-local-dictation-server"
if mkdir -p "$LINK_DIR" 2>/dev/null && ln -sf "$PREFIX/bin-local-dictation-server" "$LINK_DIR/local-dictation-server" 2>/dev/null; then
  info "linked $LINK_DIR/local-dictation-server"
  # A link into a directory that is not on PATH looks like a working install
  # right up until the first command is not found.
  case ":$PATH:" in
    *":$LINK_DIR:"*) COMMAND="local-dictation-server" ;;
    *) info "note: $LINK_DIR is not on your PATH — add it, or use the full path below" ;;
  esac
else
  info "could not link into $LINK_DIR; use the full path below"
fi

if [[ "$USER_MODE" == "1" ]]; then
  EDIT_PREFIX=""
  POSTURE="The configs are set to 127.0.0.1 with TLS off, which is the posture for
a prefix you own. To serve other machines, set server.host to 0.0.0.0 and put
certificates in $PREFIX/tls — see docs/server-usage.md."
else
  EDIT_PREFIX="sudo "
  POSTURE="TLS is on and expects certificates under $PREFIX/tls. For a first run
on a trusted network, set security.tls_certificate and tls_private_key to null
in both configs and turn off require_client_certificate."
fi

cat <<NEXT

Installed under $PREFIX.

Next:

  1. Install a model. The configs point at large-v3-turbo, so this command
     needs no config change at all (1.5 GB; large-v3 is 2.9 GB):

       $PREFIX/app/scripts/fetch-model.sh large-v3-turbo --dest $PREFIX/models

  2. Check, then start. \`check\` opens every file the configs name, so a wrong
     path is a message here rather than a server that dies on startup:

       $COMMAND check all
       $COMMAND start all
       $COMMAND health all

  To change a setting:

       $EDIT_PREFIX\$EDITOR $PREFIX/config/server-ko.yaml

$POSTURE

Full instructions: $PREFIX/docs/server-install.md
NEXT
