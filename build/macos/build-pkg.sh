#!/usr/bin/env bash
#
# Build the macOS installer package.
#
#   build/macos/build-pkg.sh [--version 0.1.0] [--output dist]
#                            [--sign-app "Developer ID Application: ..."]
#                            [--sign-pkg "Developer ID Installer: ..."]
#                            [--notarize-profile <keychain-profile>]
#
# Produces dist/LocalDictation-<version>.pkg, which installs the app into
# /Applications. Also builds a .dmg for people who prefer drag-and-drop.
#
# No model is included. The installer's closing screen says how to get one.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="0.1.0"
OUTPUT="$ROOT/dist"
SIGN_APP=""
SIGN_PKG=""
NOTARIZE_PROFILE=""
IDENTIFIER="com.local-dictation.client"

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
warn() { printf 'warning: %s\n' "$*" >&2; }
info() { printf '==> %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)           VERSION="${2:?}"; shift ;;
    --output)            OUTPUT="${2:?}"; shift ;;
    --sign-app)          SIGN_APP="${2:?}"; shift ;;
    --sign-pkg)          SIGN_PKG="${2:?}"; shift ;;
    --notarize-profile)  NOTARIZE_PROFILE="${2:?}"; shift ;;
    -h|--help) sed -n '3,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

[[ "$(uname -s)" == "Darwin" ]] || die "this script must run on macOS"
command -v pkgbuild >/dev/null || die "pkgbuild not found (install the Xcode command line tools)"

APP="$OUTPUT/Local Dictation.app"
PKG="$OUTPUT/LocalDictation-$VERSION.pkg"
DMG="$OUTPUT/LocalDictation-$VERSION.dmg"

build_args=(--version "$VERSION" --output "$OUTPUT")
[[ -n "$SIGN_APP" ]] && build_args+=(--sign "$SIGN_APP")
info "building the app bundle"
"$ROOT/build/macos/build-app.sh" "${build_args[@]}"

# pkgbuild copies whatever is under --root, so the staging directory holds
# exactly one thing: the bundle.
STAGE="$OUTPUT/.pkg-root"
rm -rf "$STAGE"; mkdir -p "$STAGE"
# ditto, not cp -R: cp writes ._AppleDouble sidecars for extended attributes,
# and those end up in the payload as junk files next to every real one.
ditto "$APP" "$STAGE/$(basename "$APP")"
# Strip extended attributes. pkgbuild stores them as ._AppleDouble entries in
# the payload, so a bundle carrying provenance or quarantine flags doubles its
# file count for nothing. Signatures live in the binary, not in xattrs.
xattr -cr "$STAGE"

info "building the component package"
COMPONENT="$OUTPUT/.LocalDictation-component.pkg"
pkgbuild \
  --root "$STAGE" \
  --identifier "$IDENTIFIER" \
  --version "$VERSION" \
  --install-location /Applications \
  "$COMPONENT"

info "writing the distribution definition"
DISTRIBUTION="$OUTPUT/.distribution.xml"
cat > "$DISTRIBUTION" <<XML
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>Local Dictation $VERSION</title>
    <organization>com.local-dictation</organization>
    <options customize="never" require-scripts="false" hostArchitectures="arm64,x86_64"/>
    <welcome file="welcome.html" mime-type="text/html"/>
    <conclusion file="conclusion.html" mime-type="text/html"/>
    <volume-check>
        <allowed-os-versions><os-version min="12.0"/></allowed-os-versions>
    </volume-check>
    <pkg-ref id="$IDENTIFIER" version="$VERSION" onConclusion="none">$(basename "$COMPONENT")</pkg-ref>
    <choices-outline><line choice="default"/></choices-outline>
    <choice id="default" title="Local Dictation"><pkg-ref id="$IDENTIFIER"/></choice>
</installer-gui-script>
XML

RESOURCES="$OUTPUT/.pkg-resources"
rm -rf "$RESOURCES"; mkdir -p "$RESOURCES"
sed "s/@VERSION@/$VERSION/g" "$ROOT/build/macos/resources/welcome.html"    > "$RESOURCES/welcome.html"
sed "s/@VERSION@/$VERSION/g" "$ROOT/build/macos/resources/conclusion.html" > "$RESOURCES/conclusion.html"

info "building the installer"
product_args=(
  --distribution "$DISTRIBUTION"
  --package-path "$(dirname "$COMPONENT")"
  --resources "$RESOURCES"
)
[[ -n "$SIGN_PKG" ]] && product_args+=(--sign "$SIGN_PKG")
productbuild "${product_args[@]}" "$PKG"

rm -rf "$STAGE" "$RESOURCES" "$DISTRIBUTION" "$COMPONENT"

if [[ -n "$NOTARIZE_PROFILE" ]]; then
  info "submitting for notarisation"
  xcrun notarytool submit "$PKG" --keychain-profile "$NOTARIZE_PROFILE" --wait
  # Stapling lets the package install on a machine with no internet, which is
  # the entire point on a closed network.
  xcrun stapler staple "$PKG"
fi

# The disk image is a convenience for people who prefer drag-and-drop; the
# package is the installer. hdiutil needs to attach a device to build one, which
# is unreliable inside a sandboxed CI runner and fails intermittently there. So
# it retries, and if it still cannot, the release goes out without a .dmg rather
# than not going out.
info "building the disk image"
DMG_STAGE="$OUTPUT/.dmg-root"
rm -rf "$DMG_STAGE"; mkdir -p "$DMG_STAGE"
ditto "$APP" "$DMG_STAGE/$(basename "$APP")"
ln -s /Applications "$DMG_STAGE/Applications"
cp "$ROOT/docs/model-setup.md" "$DMG_STAGE/Install a model first - model-setup.md"

DMG_BUILT=0
for attempt in 1 2 3; do
  rm -f "$DMG"
  if output="$(hdiutil create -volname "Local Dictation $VERSION" \
                 -srcfolder "$DMG_STAGE" -ov -format UDZO "$DMG" 2>&1)"; then
    DMG_BUILT=1
    break
  fi
  printf 'hdiutil attempt %d failed:\n%s\n' "$attempt" "$output" >&2
  sleep 5
done
rm -rf "$DMG_STAGE"

if [[ "$DMG_BUILT" != "1" ]]; then
  rm -f "$DMG"
  warn "could not build the disk image; shipping the package only"
fi

info "built:"
artifacts=("$PKG")
[[ "$DMG_BUILT" == "1" ]] && artifacts+=("$DMG")
ls -lh "${artifacts[@]}" | sed 's/^/    /'
printf '\n    shasum -a 256:\n'
shasum -a 256 "${artifacts[@]}" | sed 's/^/    /'
