#!/usr/bin/env bash
# Install and run the systemd units from deploy/systemd on a real systemd
# host, as docs/admin-guide.md "Running the servers with systemd" says:
# relay, maintainer node (plus its backup service), and a community server
# created with `systemd-run` as the service's own user, its storage key
# moved off the data directory through the credential drop-in. Checks that
# every service runs as a dynamic non-root user, answers its health checks,
# and that the backup service writes a backup. Needs root (CI: sudo on an
# Ubuntu runner, job "servers"); DIR holds the linux binaries for this host.
#
#   sudo scripts/test-systemd.sh dist/servers/linux-amd64
set -euo pipefail

BIN=${1:?usage: test-systemd.sh BINDIR}
REPO=$(cd "$(dirname "$0")/.." && pwd)
UNITS=/etc/systemd/system

cleanup() {
	systemctl stop openarcanum-community openarcanum-meta openarcanum-relay openarcanum-meta-backup.timer 2>/dev/null || true
}
trap cleanup EXIT

install -m 0755 "$BIN/relay" "$BIN/community" "$BIN/meta" /usr/local/bin/
install -d -m 0755 /etc/openarcanum
# The example configs, pointed at this machine.
sed -e 's|"registry_urls": \[[^]]*\]|"registry_urls": ["http://127.0.0.1:7100"]|' \
	-e 's|wss://relay.example.invalid/ws|ws://127.0.0.1:7000/ws|' "$REPO/deploy/config/relay.json" >/etc/openarcanum/relay.json
# The relay registers at ws://127.0.0.1: allowed on this test machine only.
sed -e 's|"peers": \[[^]]*\]|"peers": [], "allow_private_relays": true|' "$REPO/deploy/config/meta.json" >/etc/openarcanum/meta.json
cp "$REPO"/deploy/systemd/*.service "$REPO"/deploy/systemd/*.timer "$UNITS/"
systemctl daemon-reload

systemctl start openarcanum-meta openarcanum-relay
wait_ok() {
	for _ in $(seq 1 60); do
		if /usr/local/bin/relay healthcheck "$1" >/dev/null 2>&1; then
			echo "ok: $1"
			return 0
		fi
		sleep 1
	done
	echo "never healthy: $1" >&2
	return 1
}
wait_ok http://127.0.0.1:7100/healthz
wait_ok http://127.0.0.1:7101/healthz
wait_ok http://127.0.0.1:7000/healthz
wait_ok http://127.0.0.1:7001/metrics

systemd-run --wait --collect -p DynamicUser=yes -p User=openarcanum-community -p StateDirectory=openarcanum-community \
	/usr/local/bin/community init -data /var/lib/openarcanum-community -relay ws://127.0.0.1:7000/ws \
	-maintainer http://127.0.0.1:7100 -metrics-listen 127.0.0.1:7201 -install vps
# The storage key moves off the data directory, handed over as a credential.
install -d -m 0700 /etc/openarcanum/credentials
mv /var/lib/private/openarcanum-community/storage.key /etc/openarcanum/credentials/community-storage.key
install -D -m 0644 "$REPO/deploy/systemd/openarcanum-community-storage-key.conf" "$UNITS/openarcanum-community.service.d/storage-key.conf"
systemctl daemon-reload
systemctl start openarcanum-community
wait_ok http://127.0.0.1:7201/healthz
wait_ok http://127.0.0.1:7201/readyz

for s in openarcanum-relay openarcanum-meta openarcanum-community; do
	pid=$(systemctl show -p MainPID --value "$s")
	uid=$(awk '/^Uid:/ {print $2}' "/proc/$pid/status")
	if [ "$uid" = 0 ]; then
		echo "$s runs as root" >&2
		exit 1
	fi
	echo "$s: uid $uid, exposure $(systemd-analyze security "$s" | awk '/Overall exposure level/ {print $5}')"
done

systemctl start openarcanum-meta-backup.service
ls /var/lib/private/openarcanum-meta/backups/meta-*.tar
journalctl -u openarcanum-community --no-pager | tail -5
echo "systemd units: all services healthy"
