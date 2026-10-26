#!/usr/bin/env bash
# Runs a local network: relay :7000 (registered with the maintainer node),
# maintainer node :7100 and one community server, with data in .dev/.
# The community server has no listening port: it only connects outward to the
# relay (docs/architecture.md). On first run it prints a claim code; paste it into
# the client under + (Add a community) → Host on your own server.
#
#   scripts/dev-up.sh          start everything; Ctrl+C stops it
#   scripts/dev-up.sh reset    delete .dev/ first
set -euo pipefail
cd "$(dirname "$0")/.."
BIN=bin
DEV=.dev

if [[ "${1:-}" == "reset" ]]; then rm -rf "$DEV"; fi
for b in relay meta community; do
  [[ -x "$BIN/$b" ]] || { echo "missing $BIN/$b: run 'make build'" >&2; exit 1; }
done
mkdir -p "$DEV/meta" "$DEV/logs"

pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# The relay registers at ws://127.0.0.1:7000/ws: allowed in development only.
OPENARCANUM_META_LISTEN=127.0.0.1:7100 OPENARCANUM_META_DATA_DIR="$DEV/meta" OPENARCANUM_META_ALLOW_PRIVATE_RELAYS=true \
  "$BIN/meta" >"$DEV/logs/meta.log" 2>&1 &
pids+=($!)

OPENARCANUM_RELAY_LISTEN=127.0.0.1:7000 OPENARCANUM_RELAY_REGION=dev \
OPENARCANUM_RELAY_REGISTRY_URLS=http://127.0.0.1:7100 OPENARCANUM_RELAY_PUBLIC_URL=ws://127.0.0.1:7000/ws \
OPENARCANUM_RELAY_REGISTRY_KEY_FILE="$DEV/relay-registry.key" \
  "$BIN/relay" >"$DEV/logs/relay.log" 2>&1 &
pids+=($!)

if [[ ! -f "$DEV/community/server.key" ]]; then
  "$BIN/community" init -data "$DEV/community" -relay ws://127.0.0.1:7000/ws -maintainer http://127.0.0.1:7100 | tee "$DEV/claim.txt"
  echo
fi
"$BIN/community" run -data "$DEV/community" >"$DEV/logs/community.log" 2>&1 &
pids+=($!)

echo "relay     ws://127.0.0.1:7000/ws   (log: $DEV/logs/relay.log)"
echo "meta      http://127.0.0.1:7100    (log: $DEV/logs/meta.log)"
echo "community data in $DEV/community   (log: $DEV/logs/community.log)"
[[ -f "$DEV/claim.txt" ]] && echo "claim code: $(grep '^openarcanum-claim:' "$DEV/claim.txt")"
echo "client:   cd clients/desktop && uv run python -m openarcanum --home ../../.dev/client-a --maintainer http://127.0.0.1:7100"
echo "Ctrl+C to stop."
wait
