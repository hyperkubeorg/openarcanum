#!/usr/bin/env bash
# Check a built Windows installer on a CI runner (docs/packaging.md), from the
# repository root:
#
#   clients/desktop/packaging/windows/test_installer.sh clients/desktop/dist/OpenArcanum-Setup-2026.10.0.exe
#
# Installs it per user into a scratch folder, checks the openarcanum:// scheme
# registration, runs the installed app's --smoke test, installs it again
# silently (what the auto-updater does) and checks the app is restarted,
# then uninstalls and checks the program and the registration are gone.
set -euo pipefail

setup="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
ROOT="$(cd "$(dirname "$0")/../../../.." && pwd)"
dir="$(mktemp -d)/OpenArcanum"
key='HKCU\Software\Classes\openarcanum\shell\open\command'
# Pass /FLAGS and registry paths through untouched (Git Bash would rewrite them
# as paths); paths handed to Windows programs go through cygpath -w instead.
export MSYS2_ARG_CONV_EXCL='*' MSYS_NO_PATHCONV=1

install() {
  "$setup" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /CURRENTUSER "/DIR=$(cygpath -w "$dir")" \
    "/LOG=$(cygpath -w "$dir.log")"
}

echo "== install"
install
[ -f "$dir/OpenArcanum.exe" ] && [ -f "$dir/unins000.exe" ] || { cat "$dir.log"; exit 1; }
reg query "$key" /ve | tee /dev/stderr | grep -qi 'OpenArcanum.exe" -- "%1"'

echo "== smoke run of the installed app"
OPENARCANUM_BUNDLE_EXE="$(cygpath -w "$dir/OpenArcanum.exe")" make -C "$(cygpath -w "$ROOT")" bundle-smoke

echo "== silent reinstall (the auto-updater's upgrade) restarts the app"
install
for _ in $(seq 60); do
  tasklist /FI "IMAGENAME eq OpenArcanum.exe" | grep -qi openarcanum.exe && break
  sleep 1
done
tasklist /FI "IMAGENAME eq OpenArcanum.exe" | grep -qi openarcanum.exe || { echo "the app was not restarted"; exit 1; }
taskkill /F /IM OpenArcanum.exe || true
sleep 2

echo "== uninstall"
"$dir/unins000.exe" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
# The uninstaller re-runs itself from a temporary copy and returns at once.
for _ in $(seq 120); do
  [ -e "$dir/OpenArcanum.exe" ] || break
  sleep 1
done
[ ! -e "$dir/OpenArcanum.exe" ] || { echo "OpenArcanum.exe is still installed"; exit 1; }
if reg query "$key" /ve > /dev/null 2>&1; then
  echo "the openarcanum:// registration is still there"
  exit 1
fi
echo "installer checks passed"
