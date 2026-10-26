#!/usr/bin/env bash
# Check a built .dmg on a CI runner (docs/packaging.md), from the repository
# root:
#
#   clients/desktop/packaging/macos/test_dmg.sh clients/desktop/dist/OpenArcanum-2026.10.0.dmg
#
# Mounts it read-only, checks it holds exactly OpenArcanum.app (what the
# auto-updater installs) and the /Applications link, checks the Info.plist
# entries calls and invite links need, and runs the app's --smoke test from
# the mounted image.
set -euo pipefail

dmg="$1"
ROOT="$(cd "$(dirname "$0")/../../../.." && pwd)"
mnt="$(mktemp -d)/mnt"
mkdir -p "$mnt"
hdiutil attach -readonly -nobrowse -noautoopen -mountpoint "$mnt" "$dmg"
trap 'hdiutil detach "$mnt" -force || true' EXIT

apps=("$mnt"/*.app)
[ "${#apps[@]}" -eq 1 ] && [ "$(basename "${apps[0]}")" = "OpenArcanum.app" ] || { ls -la "$mnt"; exit 1; }
[ "$(readlink "$mnt/Applications")" = "/Applications" ]

plist="$mnt/OpenArcanum.app/Contents/Info.plist"
[ "$(plutil -extract CFBundleURLTypes.0.CFBundleURLSchemes.0 raw "$plist")" = "openarcanum" ]
for k in NSMicrophoneUsageDescription NSCameraUsageDescription NSScreenCaptureUsageDescription; do
  plutil -extract "$k" raw "$plist" > /dev/null
done
# The release version (VERSION): numbers only in the bundle versions.
if [ -n "${VERSION:-}" ]; then
  [ "$(plutil -extract CFBundleShortVersionString raw "$plist")" = "${VERSION%%-*}" ]
  [ "$(plutil -extract OpenArcanumReleaseVersion raw "$plist")" = "$VERSION" ]
fi
codesign --verify --deep --strict "$mnt/OpenArcanum.app"

OPENARCANUM_BUNDLE_EXE="$mnt/OpenArcanum.app/Contents/MacOS/OpenArcanum" make -C "$ROOT" bundle-smoke
echo "dmg checks passed"
