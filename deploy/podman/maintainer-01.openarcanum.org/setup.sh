#!/bin/bash
# Set up the maintainer node on maintainer-01.openarcanum.org (Rocky Linux).
# Run as root from a copy of deploy/podman:
#   bash /root/openarcanum-podman/maintainer-01.openarcanum.org/setup.sh
# Safe to run again: it only changes what differs. README.md explains every step.
set -Eeuo pipefail

HOST_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
. "$HOST_DIR/../lib/common.sh"

FQDN=maintainer-01.openarcanum.org
SERVICE=openarcanum-maintainer
APP_DIR=maintainer
CONFIG_NAME=meta.json
DATA_DIRS="data acme"
TCP_PORTS="443/tcp"
UDP_PORTS=""
FORWARD_PORTS=""
EXTRA_UNITS="openarcanum-maintainer-backup.service openarcanum-maintainer-backup.timer openarcanum-maintainer-backup-sync.service"
EXTRA_TIMERS="openarcanum-maintainer-backup.timer"

meta_cmd() { oa_podman exec "$SERVICE" meta "$@"; }

host_prechecks() {
    check_dns "$FQDN"
}

host_checks() {
    check_tls "$FQDN" /healthz
    check_public "https://$FQDN/healthz"

    say "Check: relay registry answers (GET /v1/relays)"
    curl -fsS -o /dev/null --max-time 10 -H "OpenArcanum-Protocol: 1" --resolve "$FQDN:443:127.0.0.1" "https://$FQDN/v1/relays"
    info "ok (relays appear here once they register)"

    say "Node facts"
    meta_cmd id -data /data | sed 's/^/    /'
    meta_cmd directory status -data /data -config /etc/openarcanum/meta.json | sed 's/^/    /'

    say "Backups"
    oa_systemctl list-timers --no-pager openarcanum-maintainer-backup.timer | sed 's/^/    /'
    if [ -f "$OA_HOME/maintainer/backup-remote/env" ]; then
        info "backups go to /home/openarcanum/maintainer/data/backups and are copied after each backup ($(grep -o 'BACKUP_REMOTE=.*' "$OA_HOME/maintainer/backup-remote/env"))"
    else
        info "backups go to /home/openarcanum/maintainer/data/backups; copy them off this machine (README.md, 'Copying backups to another machine')"
    fi
}

setup_main
