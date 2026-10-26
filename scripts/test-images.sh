#!/usr/bin/env bash
# Run the server images built from deploy/container/Containerfile and check
# what docs/admin-guide.md "Container images" promises: each runs as the
# non-root user 65532, its HEALTHCHECK turns healthy, meta and community
# keep their data in the /data volume (community's storage key in /keys), `community init` and `meta backup`
# work inside the images. CI runs it (job "servers") after building
# openarcanum-{relay,meta,community,site}:ci; it needs docker (or ENGINE=podman).
set -euo pipefail

ENGINE=${ENGINE:-docker}
TAG=${TAG:-ci}
NET=openarcanum-ci-$$
cleanup() {
	"$ENGINE" rm -f "relay-$$" "meta-$$" "community-$$" "site-$$" >/dev/null 2>&1 || true
	"$ENGINE" volume rm -f "meta-data-$$" "community-data-$$" "community-keys-$$" "site-data-$$" "site-keys-$$" >/dev/null 2>&1 || true
	"$ENGINE" network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_healthy() {
	local name=$1 status=
	for _ in $(seq 1 90); do
		status=$("$ENGINE" inspect --format '{{.State.Health.Status}}' "$name")
		if [ "$status" = healthy ]; then
			echo "$name: healthy"
			return 0
		fi
		sleep 2
	done
	echo "$name never became healthy (last: $status)" >&2
	"$ENGINE" logs "$name" >&2 || true
	return 1
}

check_user() {
	local image=$1 user
	user=$("$ENGINE" image inspect --format '{{.Config.User}}' "$image")
	if [ "$user" != "65532:65532" ]; then
		echo "$image runs as '$user', expected 65532:65532" >&2
		return 1
	fi
}

"$ENGINE" network create "$NET" >/dev/null
for s in relay meta community site; do check_user "openarcanum-$s:$TAG"; done

"$ENGINE" run -d --name "relay-$$" --network "$NET" --network-alias relay \
	-e OPENARCANUM_RELAY_MEDIA_UDP_PORT=0 "openarcanum-relay:$TAG" >/dev/null
wait_healthy "relay-$$"

"$ENGINE" volume create "meta-data-$$" >/dev/null
"$ENGINE" run -d --name "meta-$$" --network "$NET" --network-alias meta \
	-v "meta-data-$$:/data" "openarcanum-meta:$TAG" >/dev/null
wait_healthy "meta-$$"
# An online backup from a second container on the same volume.
"$ENGINE" run --rm -v "meta-data-$$:/data" "openarcanum-meta:$TAG" backup -data /data | tee /dev/stderr | grep -q '^/data/backups/meta-'

# The website: an admin made in the image (passphrase from a pipe), then
# serving with its key in /keys, and an online backup.
"$ENGINE" volume create "site-data-$$" >/dev/null
"$ENGINE" volume create "site-keys-$$" >/dev/null
printf '%s\n' 'a passphrase for the image test' | "$ENGINE" run --rm -i -v "site-data-$$:/data" -v "site-keys-$$:/keys" \
	"openarcanum-site:$TAG" admin add ci | grep -q 'Admin ci added'
"$ENGINE" run -d --name "site-$$" -v "site-data-$$:/data" -v "site-keys-$$:/keys:ro" "openarcanum-site:$TAG" >/dev/null
wait_healthy "site-$$"
"$ENGINE" run --rm -v "site-data-$$:/data" -v "site-keys-$$:/keys:ro" "openarcanum-site:$TAG" backup \
	| tee /dev/stderr | grep -q '^/data/backups/site-backup-'

"$ENGINE" volume create "community-data-$$" >/dev/null
"$ENGINE" volume create "community-keys-$$" >/dev/null
"$ENGINE" run --rm -v "community-data-$$:/data" -v "community-keys-$$:/keys" "openarcanum-community:$TAG" \
	init -data /data -relay ws://relay:7000/ws -maintainer http://meta:7100 -install vps | grep -q '^openarcanum-claim:'
# The server reads its storage key from the key volume (mounted read-only).
"$ENGINE" run -d --name "community-$$" --network "$NET" -v "community-data-$$:/data" -v "community-keys-$$:/keys:ro" \
	"openarcanum-community:$TAG" >/dev/null
wait_healthy "community-$$"
# Attached to the relay: ready, checked from inside with the binary itself.
for _ in $(seq 1 30); do
	if "$ENGINE" exec "community-$$" /usr/local/bin/community healthcheck http://127.0.0.1:7201/readyz; then
		echo "community: ready"
		exit 0
	fi
	sleep 2
done
echo "community never attached to the relay" >&2
"$ENGINE" logs "community-$$" >&2
exit 1
