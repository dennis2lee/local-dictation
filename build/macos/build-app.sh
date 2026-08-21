#!/usr/bin/env bash
#
# Build "Local Dictation.app".
#
#   build/macos/build-app.sh [--version 0.1.0] [--output dist] [--sign "Developer ID Application: ..."]
#
# The bundle carries the client binary, the Python server source, and the docs
# that explain how to install a model. It deliberately does NOT carry a model:
# the smallest is 145 MB and the recommended one is 1.5 GB, they have their own
# licence, and every site mirrors them differently. See docs/model-setup.md.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="0.1.0"
OUTPUT="$ROOT/dist"
IDENTITY=""
ARCHS="arm64,amd64"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:?}"; shift ;;
    --output)  OUTPUT="${2:?}";  shift ;;
    --sign)    IDENTITY="${2:?}"; shift ;;
    --arch)    ARCHS="${2:?}";   shift ;;
    -h|--help) sed -n '3,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

[[ "$(uname -s)" == "Darwin" ]] || die "this script builds a macOS bundle and must run on macOS"

APP="$OUTPUT/Local Dictation.app"
CONTENTS="$APP/Contents"

info "cleaning $APP"
rm -rf "$APP"
mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Resources"

# --- binary ----------------------------------------------------------------
# One universal binary rather than two downloads. cgo means each slice has to be
# compiled separately and stitched with lipo; `go build` cannot do it in one go.
declare -a SLICES=()
IFS=',' read -ra WANTED <<< "$ARCHS"
for arch in "${WANTED[@]}"; do
  info "building the client for darwin/$arch"
  slice="$OUTPUT/.local-dictation-$arch"
  ( cd "$ROOT/client" && \
    CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$slice" ./cmd/local-dictation )
  SLICES+=("$slice")
done

if [[ ${#SLICES[@]} -gt 1 ]]; then
  info "merging ${#SLICES[@]} slices with lipo"
  lipo -create -output "$CONTENTS/MacOS/local-dictation" "${SLICES[@]}"
else
  cp "${SLICES[0]}" "$CONTENTS/MacOS/local-dictation"
fi
rm -f "${SLICES[@]}"
chmod +x "$CONTENTS/MacOS/local-dictation"

# --- icon ------------------------------------------------------------------
info "rendering the icon"
ICONSET="$OUTPUT/.AppIcon.iconset"
rm -rf "$ICONSET"; mkdir -p "$ICONSET"
PNGS="$OUTPUT/.icon-png"
rm -rf "$PNGS"; mkdir -p "$PNGS"
python3 "$ROOT/build/assets/make-icon.py" "$PNGS" >/dev/null

# iconutil wants this exact naming; the @2x entries are the next size up.
for size in 16 32 128 256 512; do
  cp "$PNGS/icon-$size.png" "$ICONSET/icon_${size}x${size}.png"
  cp "$PNGS/icon-$((size * 2)).png" "$ICONSET/icon_${size}x${size}@2x.png"
done
iconutil --convert icns --output "$CONTENTS/Resources/AppIcon.icns" "$ICONSET"
rm -rf "$ICONSET" "$PNGS"

# --- payload ---------------------------------------------------------------
info "copying the Python server"
mkdir -p "$CONTENTS/Resources/server"
# Source and scripts only. The server needs nothing else: no wheels, no venv,
# no model. The client builds the environment on first use.
( cd "$ROOT/server" && \
  tar --exclude='__pycache__' --exclude='*.pyc' --exclude='.venv' --exclude='.pytest_cache' \
      -cf - app config scripts pyproject.toml ) | tar -xf - -C "$CONTENTS/Resources/server"

info "copying the documentation"
mkdir -p "$CONTENTS/Resources/docs"
cp "$ROOT/docs/"*.md "$CONTENTS/Resources/docs/"
cp "$ROOT/README.md" "$CONTENTS/Resources/docs/" 2>/dev/null || true

# --- Info.plist ------------------------------------------------------------
info "writing Info.plist"
cat > "$CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>                  <string>Local Dictation</string>
    <key>CFBundleDisplayName</key>           <string>Local Dictation</string>
    <key>CFBundleExecutable</key>            <string>local-dictation</string>
    <key>CFBundleIdentifier</key>            <string>com.local-dictation.client</string>
    <key>CFBundleVersion</key>               <string>$VERSION</string>
    <key>CFBundleShortVersionString</key>    <string>$VERSION</string>
    <key>CFBundlePackageType</key>           <string>APPL</string>
    <key>CFBundleIconFile</key>              <string>AppIcon</string>
    <key>LSMinimumSystemVersion</key>        <string>12.0</string>
    <key>NSHighResolutionCapable</key>       <true/>

    <!-- Required. Without it macOS terminates the process the moment it opens
         a capture device, with no dialog and no log entry. -->
    <key>NSMicrophoneUsageDescription</key>
    <string>Local Dictation transcribes your speech on this computer. Audio is never sent anywhere.</string>

    <!-- The global shortcut and writing text at the cursor both need
         Accessibility. macOS shows this string in the permission prompt. -->
    <key>NSAppleEventsUsageDescription</key>
    <string>Local Dictation types the transcribed text into the application you are using.</string>
</dict>
</plist>
PLIST

# --- signing ---------------------------------------------------------------
if [[ -n "$IDENTITY" ]]; then
  info "signing with $IDENTITY"
  # The hardened runtime is required for notarisation; the audio-input
  # entitlement is what lets a hardened app open the microphone at all.
  ENTITLEMENTS="$OUTPUT/.entitlements.plist"
  cat > "$ENTITLEMENTS" <<'ENT'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>com.apple.security.device.audio-input</key>      <true/>
    <key>com.apple.security.cs.allow-jit</key>            <true/>
    <!-- The bundled Python is executed as a child process. -->
    <key>com.apple.security.cs.allow-unsigned-executable-memory</key> <true/>
    <key>com.apple.security.cs.disable-library-validation</key>       <true/>
</dict>
</plist>
ENT
  codesign --force --deep --options runtime --timestamp \
           --entitlements "$ENTITLEMENTS" --sign "$IDENTITY" "$APP"
  rm -f "$ENTITLEMENTS"
  codesign --verify --strict --verbose=2 "$APP"
else
  # An unsigned bundle still runs, but only after the user clears Gatekeeper by
  # hand. Say so rather than letting them find out.
  info "not signed — recipients will need to right-click > Open the first time"
fi

info "built $APP"
du -sh "$APP"
