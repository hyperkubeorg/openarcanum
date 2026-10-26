#!/usr/bin/env bash
# Build the Windows installer from the PyInstaller bundle (docs/packaging.md).
# Runs on a GitHub Windows runner in Git Bash, from the repository root, after
# `make bundle`:
#
#   VERSION=2026.10.0 clients/desktop/packaging/windows/build_installer.sh
#
# Signs the bundled OpenArcanum.exe, the installer and its uninstaller with
# signtool when WINDOWS_CERT_PFX (the code-signing certificate as base64 PFX)
# and WINDOWS_CERT_PASSWORD are set; otherwise builds them unsigned and says so
# in the workflow log. WINDOWS_TIMESTAMP_URL overrides the RFC 3161
# timestamping service. Output: clients/desktop/dist/OpenArcanum-Setup-$VERSION.exe.
set -euo pipefail

: "${VERSION:?set VERSION}"
CLIENT="$(cd "$(dirname "$0")/../.." && pwd)"
DIST="$CLIENT/dist"
TSA="${WINDOWS_TIMESTAMP_URL:-http://timestamp.digicert.com}"

win() { cygpath -w "$1"; }

# Git Bash rewrites arguments that start with / into Windows paths when it
# starts a Windows program, so ISCC's /D... switches turned into extra script
# filenames (and signtool's /f into F:/). Every path below goes through win(),
# so turn the rewriting off for the whole script.
export MSYS2_ARG_CONV_EXCL='*' MSYS_NO_PATHCONV=1

iscc="$(command -v iscc || command -v ISCC || true)"
for candidate in "/c/Program Files (x86)/Inno Setup 6/ISCC.exe" "/c/Program Files/Inno Setup 6/ISCC.exe"; do
  [ -z "$iscc" ] && [ -x "$candidate" ] && iscc="$candidate"
done
if [ -z "$iscc" ]; then
  echo "Inno Setup not found; installing it with Chocolatey"
  choco install innosetup --no-progress -y
  iscc="/c/Program Files (x86)/Inno Setup 6/ISCC.exe"
fi

args=(
  "/DAppVersion=$VERSION"
  "/DAppNumericVersion=${VERSION%%-*}"
  "/DSourceDir=$(win "$DIST/OpenArcanum")"
  "/DOutputDir=$(win "$DIST")"
  "/DIconFile=$(win "$CLIENT/build/icons/openarcanum.ico")"
)

if [ -n "${WINDOWS_CERT_PFX:-}" ] && [ -n "${WINDOWS_CERT_PASSWORD:-}" ]; then
  signtool="$(ls "/c/Program Files (x86)/Windows Kits/10/bin/"*/x64/signtool.exe | sort -V | tail -n 1)"
  pfx="$(mktemp -d)/cert.pfx"
  trap 'rm -f "$pfx"' EXIT
  printf '%s' "$WINDOWS_CERT_PFX" | base64 -d > "$pfx"
  sign=("$signtool" sign /f "$(win "$pfx")" /p "$WINDOWS_CERT_PASSWORD" /fd sha256 /tr "$TSA" /td sha256)
  "${sign[@]}" "$(win "$DIST/OpenArcanum/OpenArcanum.exe")"
  args+=("/DSignInstaller"
    "/Sopenarcanumsign=\$q$(win "$signtool")\$q sign /f \$q$(win "$pfx")\$q /p \$q$WINDOWS_CERT_PASSWORD\$q /fd sha256 /tr \$q$TSA\$q /td sha256 \$f")
  echo "Signing the app, the installer and the uninstaller"
else
  echo "::notice title=Unsigned Windows installer::WINDOWS_CERT_PFX / WINDOWS_CERT_PASSWORD are not set, so the installer is unsigned (SmartScreen will warn)."
  signtool=""
fi

"$iscc" "${args[@]}" "$(win "$CLIENT/packaging/windows/openarcanum.iss")"

setup="$DIST/OpenArcanum-Setup-$VERSION.exe"
[ -f "$setup" ] || { echo "ISCC did not produce $setup"; exit 1; }
if [ -n "$signtool" ]; then
  "$signtool" verify /pa "$(win "$setup")"
  "$signtool" verify /pa "$(win "$DIST/OpenArcanum/OpenArcanum.exe")"
fi
echo "built $setup"
