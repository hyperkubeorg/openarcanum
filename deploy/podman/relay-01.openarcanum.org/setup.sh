#!/bin/bash
# Set up the relay on relay-01.openarcanum.org (Rocky Linux).
# Run as root from a copy of deploy/podman, after the maintainer node is up:
#   bash /root/openarcanum-podman/relay-01.openarcanum.org/setup.sh
# Safe to run again: it only changes what differs. README.md explains every step.
set -Eeuo pipefail

HOST_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
. "$HOST_DIR/../lib/common.sh"

FQDN=relay-01.openarcanum.org
MAINTAINER=maintainer-01.openarcanum.org
SERVICE=openarcanum-relay
APP_DIR=relay
CONFIG_NAME=relay.json
DATA_DIRS="keys acme"
TCP_PORTS="443/tcp"
UDP_PORTS="7000/udp"
FORWARD_PORTS=""
EXTRA_UNITS=""
EXTRA_TIMERS=""

host_prechecks() {
    check_dns "$FQDN"
    if [ "$DNS_LOCAL" != yes ]; then
        warn "behind NAT the relay offers its private address for call media. Put the public address in
    \"media_public_ips\" in relay.json next to this script and run it again (README.md, 'Calls behind NAT')."
    fi
    say "Check: the maintainer node answers (https://$MAINTAINER/healthz)"
    if curl -fsS -o /dev/null --max-time 10 "https://$MAINTAINER/healthz"; then
        info "ok"
    else
        warn "$MAINTAINER does not answer yet; the relay will keep sending heartbeats, but set up the maintainer first"
    fi
}

host_checks() {
    check_tls "$FQDN" /healthz
    check_public "https://$FQDN/healthz"

    say "Check: the relay is listed at https://$MAINTAINER/v1/relays"
    info "the maintainer lists it after a heartbeat and a proof-of-control fetch of https://$FQDN/ws (up to 3 minutes)"
    local _
    for _ in $(seq 1 36); do
        if curl -fsS --max-time 10 "https://$MAINTAINER/v1/relays" 2>/dev/null | grep -aF "$FQDN" >/dev/null; then
            info "listed"
            say "Calls"
            info "media port 7000/udp is open in firewalld; test a call from two clients on different networks"
            return 0
        fi
        sleep 5
    done
    show_logs
    die "the relay is not listed at $MAINTAINER. See README.md, 'Troubleshooting: relay not listed'."
}

setup_main
