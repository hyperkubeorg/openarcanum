#!/bin/bash
# Set up the website on openarcanum.org (Rocky Linux).
# Run as root from a copy of deploy/podman:
#   bash /root/openarcanum-podman/openarcanum.org/setup.sh
# Safe to run again: it only changes what differs. README.md explains every step.
set -Eeuo pipefail

HOST_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
. "$HOST_DIR/../lib/common.sh"

FQDN=openarcanum.org
SERVICE=openarcanum-site
APP_DIR=site
CONFIG_NAME=site.json
DATA_DIRS="data keys acme"
TCP_PORTS="443/tcp"
UDP_PORTS=""
# Port 80 only redirects to https; the site listens for it on 7380.
FORWARD_PORTS="port=80:proto=tcp:toport=7380"
EXTRA_UNITS="openarcanum-site-backup.service openarcanum-site-backup.timer openarcanum-site-backup-sync.service"
BACKUP_TIMERS="openarcanum-site-backup.timer"

host_prechecks() {
    check_dns "$FQDN"
}

host_checks() {
    check_tls "$FQDN" /
    check_public "https://$FQDN/"

    say "Check: the home page is served"
    if curl -fsS --max-time 10 --resolve "$FQDN:443:127.0.0.1" "https://$FQDN/" | grep -i '<html' >/dev/null; then
        info "https://$FQDN/ returns the home page"
    else
        die "https://$FQDN/ did not return an HTML page"
    fi

    say "Check: the storage key exists"
    if [ -s "$OA_HOME/site/keys/storage.key" ]; then
        info "$OA_HOME/site/keys/storage.key is there (contents not shown). Keep a copy off this machine: backups need it."
    else
        die "$OA_HOME/site/keys/storage.key is missing; the site creates it on first start"
    fi

    say "Backups"
    if [ "$(backup_choice)" != on ]; then
        info "off: nothing is backed up on a schedule (BACKUPS=on bash setup.sh turns it on; README.md, 'Backups and restore')"
        return
    fi
    oa_systemctl list-timers --no-pager openarcanum-site-backup.timer | sed 's/^/    /'
    if [ -f "$OA_HOME/site/backup-remote/env" ]; then
        info "copied after each backup ($(grep -o 'BACKUP_REMOTE=.*' "$OA_HOME/site/backup-remote/env"))"
    else
        info "not copied anywhere yet: README.md, 'Copying backups to another machine'"
    fi

    say "Admin accounts"
    oa_podman exec "$SERVICE" site admin list | sed 's/^/    /'
    info "Create the first admin yourself (it asks for a passphrase, at least 12 characters):"
    info "  oa-podman exec -it $SERVICE site admin add NAME"
    info "Then turn on two-factor sign-in for it:"
    info "  oa-podman exec -it $SERVICE site admin totp NAME"
    info "Sign in at https://$FQDN/admin"
    info "Port 80 redirects to https; test it from another machine: curl -I http://$FQDN/"
}

setup_main
