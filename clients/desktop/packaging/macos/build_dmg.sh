#!/usr/bin/env bash
# Build the macOS app and its .dmg (docs/packaging.md). Runs on a GitHub macOS
# runner from the repository root:
#
#   VERSION=2026.10.0 clients/desktop/packaging/macos/build_dmg.sh
#
# With APPLE_DEVELOPER_ID_P12 (the Developer ID Application certificate and
# key as base64 .p12) and APPLE_DEVELOPER_ID_PASSWORD, the app is built signed
# with the hardened runtime (PyInstaller signs every binary with
# packaging/macos/entitlements.plist), and the .dmg is signed too. With
# APPLE_ID, APPLE_TEAM_ID and APPLE_APP_PASSWORD (an app-specific password) as
# well, the app and then the .dmg are notarized with notarytool and the
# tickets stapled. Without the certificate everything is built ad-hoc signed
# and the workflow log says so.
#
# Output: clients/desktop/dist/OpenArcanum-$VERSION.dmg holding OpenArcanum.app and a link to
# /Applications (openarcanum/updates.py expects exactly one .app in it).
set -euo pipefail

: "${VERSION:?set VERSION}"
ROOT="$(cd "$(dirname "$0")/../../../.." && pwd)"
DIST="$ROOT/clients/desktop/dist"
APP="$DIST/OpenArcanum.app"
DMG="$DIST/OpenArcanum-$VERSION.dmg"
ENTITLEMENTS="$ROOT/clients/desktop/packaging/macos/entitlements.plist"

identity=""
keychain=""
cleanup() {
  if [ -n "$keychain" ]; then security delete-keychain "$keychain" || true; fi
}
trap cleanup EXIT

if [ -n "${APPLE_DEVELOPER_ID_P12:-}" ] && [ -n "${APPLE_DEVELOPER_ID_PASSWORD:-}" ]; then
  work="$(mktemp -d)"
  keychain="$work/signing.keychain-db"
  kc_pass="$(openssl rand -hex 24)"
  printf '%s' "$APPLE_DEVELOPER_ID_P12" | base64 --decode > "$work/id.p12"
  security create-keychain -p "$kc_pass" "$keychain"
  security set-keychain-settings -lut 3600 "$keychain"
  security unlock-keychain -p "$kc_pass" "$keychain"
  security import "$work/id.p12" -k "$keychain" -P "$APPLE_DEVELOPER_ID_PASSWORD" -T /usr/bin/codesign
  rm -f "$work/id.p12"
  security set-key-partition-list -S apple-tool:,apple: -s -k "$kc_pass" "$keychain" > /dev/null
  security list-keychains -d user -s "$keychain" $(security list-keychains -d user | tr -d '"')
  identity="$(security find-identity -v -p codesigning "$keychain" | sed -n 's/.*"\(Developer ID Application: .*\)"/\1/p' | head -n 1)"
  [ -n "$identity" ] || { echo "no Developer ID Application identity in APPLE_DEVELOPER_ID_P12"; exit 1; }
  echo "Signing as: $identity"
else
  echo "::notice title=Unsigned macOS build::APPLE_DEVELOPER_ID_P12 / APPLE_DEVELOPER_ID_PASSWORD are not set, so the app and .dmg are ad-hoc signed and not notarized (Gatekeeper will block a downloaded copy)."
fi

OPENARCANUM_CODESIGN_IDENTITY="$identity" make -C "$ROOT" bundle

codesign --verify --deep --strict --verbose=2 "$APP"
if [ -n "$identity" ]; then
  # The hardened runtime and the entitlements calls need.
  codesign -d --entitlements - --xml "$APP" | grep -q com.apple.security.device.audio-input
  codesign -dv "$APP" 2>&1 | grep -q 'flags=.*runtime'
  # Library validation stays on (entitlements.plist): the smoke run in
  # test_dmg.sh then shows every library in the bundle loads under it.
  if codesign -d --entitlements - --xml "$APP" | grep -q disable-library-validation; then
    echo "::error::the app is signed with disable-library-validation"
    exit 1
  fi
  # Every Mach-O carries the app's Team ID (what library validation checks).
  team="$(codesign -dv "$APP" 2>&1 | sed -n 's/^TeamIdentifier=//p')"
  find "$APP" -type f \( -name '*.dylib' -o -name '*.so' -o -perm -u+x \) -print0 |
    while IFS= read -r -d '' f; do
      file -b "$f" | grep -q Mach-O || continue
      t="$(codesign -dv "$f" 2>&1 | sed -n 's/^TeamIdentifier=//p')"
      [ "$t" = "$team" ] || { echo "::error::$f is signed by team '$t', not '$team'"; exit 1; }
    done
fi

notarize() {
  xcrun notarytool submit "$1" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" \
    --password "$APPLE_APP_PASSWORD" --wait --timeout 30m
}
can_notarize=""
if [ -n "$identity" ] && [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ] && [ -n "${APPLE_APP_PASSWORD:-}" ]; then
  can_notarize=1
  # Notarize and staple the app itself, so a copy dragged out of the .dmg
  # passes Gatekeeper offline too.
  ditto -c -k --keepParent "$APP" "$DIST/OpenArcanum-notarize.zip"
  notarize "$DIST/OpenArcanum-notarize.zip"
  rm -f "$DIST/OpenArcanum-notarize.zip"
  xcrun stapler staple "$APP"
elif [ -n "$identity" ]; then
  echo "::notice title=Not notarized::APPLE_ID / APPLE_TEAM_ID / APPLE_APP_PASSWORD are not set, so the signed app is not notarized."
fi

stage="$(mktemp -d)"
ditto "$APP" "$stage/OpenArcanum.app"
ln -s /Applications "$stage/Applications"
rm -f "$DMG"
hdiutil create -volname "OpenArcanum $VERSION" -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$DMG"
rm -rf "$stage"

if [ -n "$identity" ]; then
  codesign --force --timestamp --sign "$identity" "$DMG"
  codesign --verify --verbose=2 "$DMG"
fi
if [ -n "$can_notarize" ]; then
  notarize "$DMG"
  xcrun stapler staple "$DMG"
  xcrun stapler validate "$DMG"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$DMG"
fi
echo "built $DMG"
