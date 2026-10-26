#!/usr/bin/env bash
# Removes what this project leaves behind (the Makefile's clean targets).
# Never touches anything outside the project directory, prints each thing it
# removes, and does nothing (successfully) when there's nothing to remove.
#
#   scripts/clean.sh paths PATH...   remove these files or folders, each only
#                                    if it is inside the project
#   scripts/clean.sh dev             remove .dev/ (app copies, desktop
#                                    profiles, dev-up.sh servers' data); asks
#                                    first, refuses while copies run
#   scripts/clean.sh images [REF]    remove the podman images this project
#                                    built (localhost/openarcanum-*, and REF)
#
# FORCE=1 skips the question and stops running copies first.
set -euo pipefail

PROJECT=$(cd "$(dirname "$0")/.." && pwd -P)
DEV="$PROJECT/.dev"

die() { echo "clean: $*" >&2; exit 1; }

# The absolute path of $1 (relative to the project) with its folder's
# symlinks resolved, so a link pointing out of the project can't lead the
# removal there; fails unless it is strictly inside the project.
inside() {
  local p=$1 dir base
  [[ $p == /* ]] || p="$PROJECT/$p"
  p=${p%/}
  dir=$(dirname "$p")
  base=$(basename "$p")
  [[ $base != . && $base != .. && -n $base ]] || return 1
  # A folder that doesn't exist has nothing in it to remove.
  [[ -d $dir ]] || { echo "$p"; return 0; }
  dir=$(cd "$dir" && pwd -P)
  case "$dir/" in
    "$PROJECT"/*) echo "$dir/$base" ;;
    *) return 1 ;;
  esac
}

remove() {
  local p real
  for p in "$@"; do
    real=$(inside "$p") || die "refusing to remove $p: it isn't inside $PROJECT"
    case "$real/" in
      "$PROJECT"/?*) ;;
      *) die "refusing to remove $p: it isn't inside $PROJECT" ;;
    esac
    if [[ -e $real || -L $real ]]; then
      echo "removing ${real#"$PROJECT"/}"
      # Files a container created (podman's mount points in .dev/run) can
      # belong to its mapped users: remove those from inside podman's
      # user namespace, where you own them.
      rm -rf -- "$real" 2>/dev/null \
        || { command -v podman >/dev/null && podman unshare rm -rf -- "$real"; } \
        || die "couldn't remove $real"
    fi
  done
}

# App copies in containers (scripts/dev-client) that are running.
running_copies() {
  command -v podman >/dev/null || return 0
  podman ps --format '{{.Names}}' 2>/dev/null | grep '^openarcanum-client-' || true
}

# Native runs (make desktop, make desktop-appimage) using a profile in .dev:
# "PID command line" per line.
running_native() {
  pgrep -a -f -- "--home $DEV/" 2>/dev/null || true
}

cmd_dev() {
  if [[ ! -e $DEV ]]; then
    echo "clean: no .dev/ to remove"
    return
  fi
  local copies native
  copies=$(running_copies)
  native=$(running_native)
  if [[ -n $copies || -n $native ]]; then
    echo "These use .dev/ and are running:"
    [[ -z $copies ]] || sed 's/^openarcanum-client-/  app copy /' <<<"$copies"
    [[ -z $native ]] || sed 's/^/  process /' <<<"$native"
    if [[ ${FORCE:-} != 1 ]]; then
      die "close them first (scripts/dev-client --stop NAME), or run with FORCE=1 to stop them and delete"
    fi
    local c pid
    for c in $copies; do
      echo "stopping $c"
      podman stop --time 10 "$c" >/dev/null || true
    done
    for pid in $(cut -d' ' -f1 <<<"$native"); do
      echo "stopping process $pid"
      kill "$pid" 2>/dev/null || true
    done
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      [[ -z $(running_native) ]] && break
      sleep 1
    done
    [[ -z $(running_native) ]] || die "a process still uses .dev/; close it and try again"
  fi
  if [[ ${FORCE:-} != 1 ]]; then
    [[ -t 0 ]] || die "not asking without a terminal: run with FORCE=1 to delete .dev/"
    echo "This deletes .dev/: every app copy's and desktop profile's keys, messages"
    echo "and settings, and the data of servers started with scripts/dev-up.sh."
    local answer
    read -r -p "Delete .dev/? [y/N] " answer
    [[ $answer == [yY] ]] || { echo "Kept .dev/."; return; }
  fi
  remove .dev
}

cmd_images() {
  if ! command -v podman >/dev/null; then
    echo "clean: podman isn't installed here, so there are no images to remove"
    return
  fi
  local extra=${1:-} ref id refs=() copies
  while read -r ref id; do
    [[ -n $ref ]] || continue
    case "$ref" in
      localhost/openarcanum-*:*) refs+=("$ref") ;;
      *) [[ -n $extra && $ref == "$extra" ]] && refs+=("$ref") ;;
    esac
  done < <(podman images --format '{{.Repository}}:{{.Tag}} {{.ID}}')
  if ((${#refs[@]} == 0)); then
    echo "clean: no localhost/openarcanum-* images to remove"
    return
  fi
  copies=$(running_copies)
  if [[ -n $copies ]]; then
    [[ ${FORCE:-} == 1 ]] || die "app copies are running ($(tr '\n' ' ' <<<"$copies")); stop them first or run with FORCE=1"
    for c in $copies; do
      echo "stopping $c"
      podman stop --time 10 "$c" >/dev/null || true
    done
  fi
  for ref in "${refs[@]}"; do echo "removing image $ref"; done
  podman rmi "${refs[@]}" >/dev/null
}

case "${1:-}" in
  paths) shift; remove "$@" ;;
  dev) cmd_dev ;;
  images) cmd_images "${2:-}" ;;
  *) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
