#!/usr/bin/env bash
# Runs the OpenArcanum app natively on this computer (no container), with its
# profile, keys, settings and logs in .dev/desktop/NAME in this project, so
# `make clean-dev` (or deleting that folder) removes every trace.
#
#   scripts/desktop.sh [OPTIONS] NAME [-- APP ARGS]
#
#   --appimage      run the AppImage (clients/desktop/dist/OpenArcanum-x86_64.AppImage,
#                   built with `make appimage` if it isn't there) instead of
#                   the code in clients/desktop/src
#   --rebuild       with --appimage: build it again first
#   --fake-media    Chromium's fake camera and microphone (a test pattern and
#                   a beep) instead of the real devices
#   --maintainer URL  maintainer node (default http://127.0.0.1:7100, the
#                   kind proxy or scripts/dev-up.sh; repeatable)
#   -n, --dry-run   print the command instead of running it
#
# From source it needs uv (which installs the Python in clients/desktop/.python-version
# itself) and the system libraries Qt WebEngine loads; it checks for both, and
# builds bin/community with Go (hosting communities on this computer).
# `make desktop NAME=me` and `make desktop-appimage NAME=me` run this.
set -euo pipefail

PROJECT=$(cd "$(dirname "$0")/.." && pwd -P)
UV=${UV:-uv}
MAKE=${MAKE:-make}
APPIMAGE="$PROJECT/clients/desktop/dist/OpenArcanum-x86_64.AppImage"
DEFAULT_MAINTAINER=http://127.0.0.1:7100
FAKE_DEVICE_FLAG=--use-fake-device-for-media-stream

die() { echo "desktop: $*" >&2; exit 1; }
usage() { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

# Shared libraries Qt WebEngine (PySide6's wheels) loads from the system, by
# the Arch package that has them; the same list as
# deploy/container/Containerfile.client.
LIBS=(
  nss:libnss3.so nspr:libnspr4.so alsa-lib:libasound.so.2 fontconfig:libfontconfig.so.1
  freetype2:libfreetype.so.6 dbus:libdbus-1.so.3 libglvnd:libGL.so.1 libglvnd:libEGL.so.1
  libxkbcommon:libxkbcommon.so.0 libxkbcommon-x11:libxkbcommon-x11.so.0
  libxcomposite:libXcomposite.so.1 libxdamage:libXdamage.so.1 libxrandr:libXrandr.so.2
  libxtst:libXtst.so.6 libxkbfile:libxkbfile.so.1 libxshmfence:libxshmfence.so.1
  libxcursor:libXcursor.so.1 libxi:libXi.so.6 xcb-util-cursor:libxcb-cursor.so.0
  xcb-util-image:libxcb-image.so.0 xcb-util-keysyms:libxcb-keysyms.so.1
  xcb-util-renderutil:libxcb-render-util.so.0 xcb-util-wm:libxcb-icccm.so.4
)

check_libs() {
  [[ $(uname -s) == Linux ]] || return 0
  [[ ${OPENARCANUM_SKIP_LIB_CHECK:-} == 1 ]] && return 0
  local ldconfig
  ldconfig=$(command -v ldconfig || echo /usr/bin/ldconfig)
  [[ -x $ldconfig ]] || { echo "desktop: no ldconfig here, so the system libraries aren't checked" >&2; return 0; }
  local cache missing_pkgs=() missing_libs=() entry
  cache=$("$ldconfig" -p 2>/dev/null) || return 0
  for entry in "${LIBS[@]}"; do
    if ! grep -qF " ${entry#*:} " <<<"$cache"; then
      missing_libs+=("${entry#*:}")
      [[ " ${missing_pkgs[*]-} " == *" ${entry%%:*} "* ]] || missing_pkgs+=("${entry%%:*}")
    fi
  done
  if ((${#missing_libs[@]})); then
    echo "desktop: Qt WebEngine needs system libraries this computer doesn't have:" >&2
    echo "  ${missing_libs[*]}" >&2
    echo "On Arch: sudo pacman -S --needed ${missing_pkgs[*]}" >&2
    echo "(elsewhere, your distribution's packages with those files; docs/kubernetes.md" >&2
    echo "\"The app on your desktop\"). OPENARCANUM_SKIP_LIB_CHECK=1 skips this check." >&2
    exit 1
  fi
  grep -qF ' libpulse.so.0 ' <<<"$cache" \
    || echo "desktop: no libpulse.so.0 (Arch: libpulse), so calls fall back to ALSA for sound" >&2
}

have_fuse() {
  [[ -e /dev/fuse ]] && { command -v fusermount3 >/dev/null || command -v fusermount >/dev/null; }
}

appimage=0 rebuild=0 fake=0 dry=0 name="" maintainers=() app_args=()
while (($#)); do
  case "$1" in
    --appimage) appimage=1 ;;
    --rebuild) rebuild=1 ;;
    --fake-media) fake=1 ;;
    --maintainer) (($# >= 2)) || die "--maintainer needs a URL"; maintainers+=("$2"); shift ;;
    --maintainer=*) maintainers+=("${1#*=}") ;;
    -n | --dry-run) dry=1 ;;
    -h | --help) usage ;;
    --) shift; app_args=("$@"); break ;;
    -*) die "unknown option $1 (scripts/desktop.sh --help)" ;;
    *) [[ -z $name ]] || die "one NAME only (app options go after --)"; name=$1 ;;
  esac
  shift
done
[[ -n $name ]] || usage
[[ $name =~ ^[a-z0-9_-]{1,40}$ ]] || die "NAME is 1-40 lowercase letters, digits, - or _"

home="$PROJECT/.dev/desktop/$name"
# The app's own --maintainer wins over the default.
if ((${#maintainers[@]} == 0)) && [[ " ${app_args[*]-} " != *" --maintainer"* ]]; then
  maintainers=("$DEFAULT_MAINTAINER")
fi
args=(--home "$home")
# ${a[@]+"${a[@]}"}: an empty array under set -u, in bash 3.2 (macOS) too.
for m in ${maintainers[@]+"${maintainers[@]}"}; do args+=(--maintainer "$m"); done
args+=(${app_args[@]+"${app_args[@]}"})

env_args=()
if ((fake)); then
  env_args=("QTWEBENGINE_CHROMIUM_FLAGS=${QTWEBENGINE_CHROMIUM_FLAGS:+$QTWEBENGINE_CHROMIUM_FLAGS }$FAKE_DEVICE_FLAG")
fi

if ((appimage)); then
  cmd=("$APPIMAGE" "${args[@]}")
  # The AppImage mounts itself with FUSE; without it, it unpacks to a
  # temporary folder and runs from there.
  if ! have_fuse; then
    env_args+=(APPIMAGE_EXTRACT_AND_RUN=1)
    fuse_note="no FUSE here, so the AppImage unpacks itself to a temporary folder each start"
  fi
else
  cmd=("$UV" run --project "$PROJECT/clients/desktop" --locked python -m openarcanum "${args[@]}")
fi
if ((${#env_args[@]})); then cmd=(env "${env_args[@]}" "${cmd[@]}"); fi

# From source, hosting a community on this computer runs bin/community
# (openarcanum/hosting.py); it's built here, as `make build` would.
GO=${GO:-go}
build_server() {
  if command -v "$GO" >/dev/null; then
    (cd "$PROJECT" && "$GO" build -o bin/ ./cmd/community) || die "building bin/community failed"
  elif [[ ! -x $PROJECT/bin/community ]]; then
    echo "desktop: no Go here and no bin/community, so hosting a community on this computer won't work" >&2
  fi
}

if ((dry)); then
  ((appimage)) && { [[ -x $APPIMAGE && $rebuild == 0 ]] || echo "$MAKE -C $PROJECT appimage"; }
  ((appimage)) || echo "desktop: would build bin/community ($GO build -o bin/ ./cmd/community)" >&2
  printf '%q ' "${cmd[@]}"
  echo
  exit 0
fi

command -v "$UV" >/dev/null || die "uv isn't installed (Arch: sudo pacman -S uv; elsewhere https://docs.astral.sh/uv/). It installs the Python the app needs by itself."
check_libs
if ((appimage)) && { ((rebuild)) || [[ ! -x $APPIMAGE ]]; }; then
  echo "desktop: building the AppImage (make appimage)…"
  "$MAKE" -C "$PROJECT" appimage
fi
[[ -z ${fuse_note:-} ]] || echo "desktop: $fuse_note"
((appimage)) || build_server
mkdir -p "$home"
echo "desktop: starting $name (data in .dev/desktop/$name); close its window to stop it"
exec "${cmd[@]}"
