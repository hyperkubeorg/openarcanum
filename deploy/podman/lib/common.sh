# Shared steps for the setup.sh scripts in deploy/podman/*/.
# Sourced, not run. Every function prints what it does and stops the
# script on the first error (set -Eeuo pipefail in the caller).
#
# The caller sets, before calling setup_main:
#   SERVICE      systemd unit and container name, e.g. openarcanum-relay
#   APP_DIR      the service's directory under /home/openarcanum, e.g. relay
#   CONFIG_NAME  the JSON config file name, e.g. relay.json; empty for a
#                service that writes its own (the community server's init)
#   DATA_DIRS    subdirectories of APP_DIR the container writes (data keys acme ...)
#   TCP_PORTS    ports to open in firewalld, e.g. "443/tcp"
#   EXTRA_UNITS  optional extra user units (backup .service/.timer) in HOST_DIR
#   EXTRA_TIMERS optional timers to enable
#   BACKUP_TIMERS optional backup timers: on only when backups are on
#                (backup_choice below), stopped and disabled otherwise
#   METRICS_PORT, METRICS_ENV, METRICS_TOKEN_DIR, METRICS_TOKEN_PATH,
#   METRICS_PUBLISH  the status listener's port, the service's environment
#                prefix, where the metrics token lives (a directory under
#                APP_DIR, and its path in the container) and whether the
#                port is published (a container on its own network): for
#                monitoring_choice below
# and defines host_checks (run after the service is up), optionally
# host_prechecks (before the image is pulled) and host_before_start (after
# the image is pulled and verified, before the service starts).

OA_USER=openarcanum
OA_HOME=/home/$OA_USER
QUADLET_DIR=$OA_HOME/.config/containers/systemd
USER_UNIT_DIR=$OA_HOME/.config/systemd/user
SYSCTL_FILE=/etc/sysctl.d/90-openarcanum.conf
MIN_PODMAN=4.9

# Scheduled backups are off unless chosen: they need room, and a copy
# somewhere other than this disk, to be worth keeping. `BACKUPS=on bash
# setup.sh` (or off) chooses, and the choice is kept in
# APP_DIR/config/backups for later runs. Prints on or off.
backup_choice() {
    local f="$OA_HOME/$APP_DIR/config/backups"
    if [ -n "${BACKUPS:-}" ]; then
        case "$BACKUPS" in on|off) ;; *) die "BACKUPS must be on or off" ;; esac
        printf '%s\n' "$BACKUPS" > "$f"
        chown "$OA_USER:$OA_USER" "$f"
    fi
    if [ "$(cat "$f" 2>/dev/null)" = on ]; then echo on; else echo off; fi
}

# A monitoring server's address (an IP address or network, such as
# 203.0.113.7 or 2001:db8::/64) may scrape /metrics; nobody else, and only
# with the token. Off unless chosen: `MONITOR_FROM=ADDRESS bash setup.sh`
# (MONITOR_FROM=off turns it off), kept in APP_DIR/config/monitoring for
# later runs. Prints the address, or nothing.
monitoring_choice() {
    local f="$OA_HOME/$APP_DIR/config/monitoring"
    if [ -n "${MONITOR_FROM:-}" ]; then
        if [ "$MONITOR_FROM" = off ]; then
            rm -f "$f"
        else
            printf '%s' "$MONITOR_FROM" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$|^[0-9A-Fa-f:]+:[0-9A-Fa-f:]*(/[0-9]{1,3})?$' ||
                die "MONITOR_FROM must be an IP address or network (or off), not $MONITOR_FROM"
            printf '%s\n' "$MONITOR_FROM" > "$f"
            chown "$OA_USER:$OA_USER" "$f"
        fi
    fi
    cat "$f" 2>/dev/null || true
}

# The lines the Quadlet file gets for monitoring (none when it's off).
monitoring_lines() {
    local from="$1"
    [ -n "$from" ] && [ -n "${METRICS_PORT:-}" ] || return 0
    echo "# Monitoring (setup.sh MONITOR_FROM): /metrics on the network, with the token."
    echo "Environment=${METRICS_ENV}_METRICS_LISTEN=0.0.0.0:${METRICS_PORT}"
    echo "Environment=${METRICS_ENV}_METRICS_TOKEN_FILE=${METRICS_TOKEN_PATH}"
    if [ -n "${METRICS_PUBLISH:-}" ]; then echo "PublishPort=${METRICS_PORT}:${METRICS_PORT}/tcp"; fi
}

# The metrics token, and the firewall: the port open to the monitoring
# server only, or to nobody.
ensure_monitoring() {
    [ -n "${METRICS_PORT:-}" ] || return 0
    local from="$1" rule token="$OA_HOME/$APP_DIR/${METRICS_TOKEN_DIR:-config}/metrics.token"
    say "Monitoring"
    # No rule for the port is the usual case (grep then finds nothing).
    { firewall-cmd --permanent --list-rich-rules | grep "port=\"$METRICS_PORT\"" || true; } | while IFS= read -r rule; do
        firewall-cmd -q --permanent --remove-rich-rule="$rule"
    done
    if [ -z "$from" ]; then
        firewall-cmd -q --reload
        info "off: /metrics answers on this machine only (MONITOR_FROM=ADDRESS bash setup.sh lets a monitoring server in; README.md, 'Monitoring')"
        return 0
    fi
    if [ ! -s "$token" ]; then
        (umask 077; od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > "$token")
        chown "$OA_USER:$OA_USER" "$token"
        chmod 0640 "$token"
        info "made the metrics token: $token (give it to Prometheus; contents not shown)"
    fi
    local family=ipv4
    case "$from" in *:*) family=ipv6 ;; esac
    firewall-cmd -q --permanent --add-rich-rule="rule family=\"$family\" source address=\"$from\" port port=\"$METRICS_PORT\" protocol=\"tcp\" accept"
    firewall-cmd -q --reload
    info "on: $METRICS_PORT/tcp open to $from only; /metrics needs the token in $token"
}

# The images are signed by the Containers workflow (keyless cosign, from
# the workflow's GitHub identity: .github/workflows/containers.yml,
# docs/release.md "Container images"). pull_image accepts a tag only when
# a signature made for one of the repository's release tags covers it,
# then pulls that digest. cosign isn't packaged for Rocky Linux: it comes
# from its GitHub release, checked against these checksums
# (cosign_checksums.txt of the release).
COSIGN_VERSION=3.1.4
COSIGN_SHA256_x86_64=02fd4fe524932ccba09f7762fce76be35e5570d2023a1093cce776eefc6b62df
COSIGN_SHA256_aarch64=51232128175fd080ae6fd3bb095901e5607d42737eefa9bbb3a4f76c20107cc8
IMAGE_SIGNER='^https://github\.com/hyperkubeorg/openarcanum/\.github/workflows/containers\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-beta\.[0-9]+)?$'
IMAGE_OIDC_ISSUER=https://token.actions.githubusercontent.com

say()  { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '    WARNING: %s\n' "$*" >&2; }
die()  { printf '\nERROR: %s\n' "$*" >&2; exit 1; }

trap 'printf "\nERROR: setup stopped at %s line %s (exit %s). Nothing after that step ran.\n" "${BASH_SOURCE[0]}" "$LINENO" "$?" >&2' ERR

oa_uid() { id -u "$OA_USER"; }

# Run a command as the openarcanum user with its systemd session.
as_oa() {
    local uid
    uid=$(oa_uid)
    (cd "$OA_HOME" && runuser -u "$OA_USER" -- env \
        HOME="$OA_HOME" \
        XDG_RUNTIME_DIR="/run/user/$uid" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
        "$@")
}
oa_podman()    { as_oa podman "$@"; }
oa_systemctl() { as_oa systemctl --user "$@"; }

version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

preflight() {
    say "Checking the machine"
    [ "$(id -u)" -eq 0 ] || die "run this script as root"
    if [ -r /etc/os-release ]; then
        # In a subshell: os-release sets NAME, which the setup scripts use.
        local os_id os_name
        # shellcheck disable=SC1091
        os_id=$(. /etc/os-release && echo "${ID:-}")
        # shellcheck disable=SC1091
        os_name=$(. /etc/os-release && echo "${PRETTY_NAME:-unknown}")
        info "OS: $os_name"
        case "$os_id" in
            rocky|rhel|almalinux|centos) ;;
            *) warn "written for Rocky Linux; continuing on ${os_id:-unknown}" ;;
        esac
    fi
    local sd
    sd=$(systemctl --version | awk 'NR==1 {print $2}')
    info "systemd $sd"
    [ "$sd" -ge 248 ] || die "systemd $sd is too old (need 248 or newer: Rocky Linux 9 or later)"
    info "SELinux: $(getenforce 2>/dev/null || echo not installed)"
}

install_packages() {
    say "Installing packages (podman, firewalld, chrony, curl, openssl, rsync)"
    dnf -y -q install podman container-selinux firewalld chrony curl openssl policycoreutils rsync
    local v
    v=$(podman version --format '{{.Client.Version}}' 2>/dev/null || podman --version | awk '{print $3}')
    info "podman $v"
    version_ge "$v" "$MIN_PODMAN" || die "podman $v is too old (need $MIN_PODMAN or newer for these Quadlet files)"
}

install_cosign() {
    say "Installing cosign $COSIGN_VERSION (checks the image signatures)"
    if [ "$(cosign version 2>/dev/null | sed -n 's/^GitVersion: *v//p')" = "$COSIGN_VERSION" ]; then
        info "already installed"
        return 0
    fi
    local arch sum rpm tmp
    arch=$(uname -m)
    case "$arch" in
        x86_64) sum=$COSIGN_SHA256_x86_64 ;;
        aarch64) sum=$COSIGN_SHA256_aarch64 ;;
        *) die "no cosign checksum for $arch in lib/common.sh" ;;
    esac
    rpm="cosign-$COSIGN_VERSION-1.$arch.rpm"
    tmp=$(mktemp -d)
    curl -fsSL --retry 3 -o "$tmp/$rpm" "https://github.com/sigstore/cosign/releases/download/v$COSIGN_VERSION/$rpm"
    if ! echo "$sum  $tmp/$rpm" | sha256sum -c --quiet; then
        rm -rf "$tmp"
        die "$rpm does not match the checksum in lib/common.sh"
    fi
    dnf -y -q install "$tmp/$rpm"
    rm -rf "$tmp"
    info "cosign $(cosign version 2>/dev/null | sed -n 's/^GitVersion: *//p')"
}

ensure_clock() {
    say "Checking the clock (chronyd)"
    systemctl enable --now chronyd >/dev/null
    local synced=no i
    for i in $(seq 1 15); do
        synced=$(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo no)
        [ "$synced" = yes ] && break
        sleep 2
    done
    if [ "$synced" = yes ]; then
        info "clock is synchronized"
    else
        warn "clock not synchronized yet (check: chronyc tracking). Signed heartbeats and TLS need a correct clock."
    fi
}

next_free_subid() {
    # First start above every range in the file, at least 100000.
    awk -F: 'BEGIN {m = 100000} {e = $2 + $3; if (e > m) m = e} END {print m}' "$1"
}

ensure_user() {
    say "Creating the user $OA_USER (no password, no sudo)"
    if id "$OA_USER" >/dev/null 2>&1; then
        info "user exists (uid $(oa_uid))"
    else
        useradd --create-home --home-dir "$OA_HOME" --shell /bin/bash \
            --comment "OpenArcanum containers" "$OA_USER"
        info "created (uid $(oa_uid))"
    fi
    chmod 0750 "$OA_HOME"
    local f start
    for f in /etc/subuid /etc/subgid; do
        touch "$f"
        if grep -q "^$OA_USER:" "$f"; then
            info "$f: $(grep "^$OA_USER:" "$f")"
        else
            start=$(next_free_subid "$f")
            if [ "$f" = /etc/subuid ]; then
                usermod --add-subuids "$start-$((start + 65535))" "$OA_USER"
            else
                usermod --add-subgids "$start-$((start + 65535))" "$OA_USER"
            fi
            info "$f: added $start-$((start + 65535))"
        fi
    done
    local count
    count=$(awk -F: -v u="$OA_USER" '$1 == u {print $3; exit}' /etc/subuid)
    [ "${count:-0}" -ge 65536 ] || die "/etc/subuid gives $OA_USER only ${count:-0} ids; the images run as uid 65532 and need 65536"

    say "Enabling lingering, so the user's services start at boot without a login"
    loginctl enable-linger "$OA_USER"
    local uid i
    uid=$(oa_uid)
    for i in $(seq 1 30); do
        [ -S "/run/user/$uid/bus" ] && break
        sleep 1
    done
    [ -S "/run/user/$uid/bus" ] || die "the user manager for $OA_USER did not start (systemctl status user@$uid)"
    info "user manager running (user@$uid.service)"
}

# Port 443 is bound by the container itself (host network) rather than
# forwarded to a high port: firewalld's forward-ports (nftables, as port 80
# on the site) apply only to traffic arriving from outside, so the host's
# own checks and health checks would need another port, and they are IPv4
# only unless rich rules are added per host. The sysctl lets any
# unprivileged process on the host bind 443 and up; openarcanum is the only
# such user here (README.md, "Port 443").
ensure_sysctl() {
    say "Letting unprivileged processes bind port 443 ($SYSCTL_FILE)"
    local want='net.ipv4.ip_unprivileged_port_start = 443'
    if [ -f "$SYSCTL_FILE" ] && grep -qx "$want" "$SYSCTL_FILE"; then
        info "already set"
    else
        printf '# Rootless podman (user %s) binds 443 directly, so client addresses\n# stay visible to the servers (rate limits). Applies to IPv6 too.\n%s\n' \
            "$OA_USER" "$want" > "$SYSCTL_FILE"
        info "written"
    fi
    sysctl -q -p "$SYSCTL_FILE"
    info "ip_unprivileged_port_start = $(sysctl -n net.ipv4.ip_unprivileged_port_start)"
}

ensure_firewall() {
    say "Opening ports in firewalld: ${TCP_PORTS:-} ${UDP_PORTS:-} ${FORWARD_PORTS:-}"
    systemctl enable --now firewalld >/dev/null
    local p
    for p in ${TCP_PORTS:-} ${UDP_PORTS:-}; do
        if firewall-cmd -q --permanent --query-port="$p"; then
            info "$p already open"
        else
            firewall-cmd -q --permanent --add-port="$p"
            info "$p opened"
        fi
    done
    for p in ${FORWARD_PORTS:-}; do
        if firewall-cmd -q --permanent --query-forward-port="$p"; then
            info "forward $p already set"
        else
            firewall-cmd -q --permanent --add-forward-port="$p"
            info "forward $p set"
        fi
    done
    # Nothing else is reachable: ssh is the only service, and a port this
    # host doesn't list (TCP_PORTS, UDP_PORTS) is closed again.
    for p in $(firewall-cmd --permanent --list-services); do
        if [ "$p" != ssh ]; then
            firewall-cmd -q --permanent --remove-service="$p"
            info "service $p closed"
        fi
    done
    firewall-cmd -q --permanent --query-service=ssh || firewall-cmd -q --permanent --add-service=ssh
    for p in $(firewall-cmd --permanent --list-ports); do
        case " ${TCP_PORTS:-} ${UDP_PORTS:-} " in
            *" $p "*) ;;
            *) firewall-cmd -q --permanent --remove-port="$p"; info "$p closed" ;;
        esac
    done
    firewall-cmd -q --reload
    info "zone $(firewall-cmd --get-default-zone): services $(firewall-cmd --list-services), ports $(firewall-cmd --list-ports)"
}

# Cloud images often start the Cockpit web console (9090/tcp) and rpcbind
# (111). Only sshd and the service set up here listen on this machine, so
# they are stopped and masked.
disable_extras() {
    say "Turning off services this machine doesn't need"
    local u
    for u in cockpit.socket cockpit.service rpcbind.socket rpcbind.service; do
        if systemctl list-unit-files -q "$u" 2>/dev/null | grep -q .; then
            if [ "$(systemctl is-enabled "$u" 2>/dev/null)" != masked ]; then
                systemctl mask -q --now "$u"
                info "$u stopped and masked"
            else
                info "$u already masked"
            fi
        fi
    done
}

# make_dir PATH MODE: owned by the user, which is uid 65532 inside the
# container (UserNS=keep-id:uid=65532,gid=65532 in the Quadlet file).
make_dir() {
    install -d -m "$2" -o "$OA_USER" -g "$OA_USER" "$1"
}

ensure_dirs() {
    say "Creating $OA_HOME/$APP_DIR and its data directories"
    make_dir "$OA_HOME/$APP_DIR" 0750
    make_dir "$OA_HOME/$APP_DIR/config" 0750
    local d
    for d in $DATA_DIRS; do
        make_dir "$OA_HOME/$APP_DIR/$d" 0700
        # Fix anything a manual copy left owned by root.
        chown -R "$OA_USER:$OA_USER" "$OA_HOME/$APP_DIR/$d"
        info "$OA_HOME/$APP_DIR/$d"
    done
    make_dir "$OA_HOME/.config" 0700
    make_dir "$OA_HOME/.config/containers" 0700
    make_dir "$QUADLET_DIR" 0700
    make_dir "$OA_HOME/.config/systemd" 0700
    make_dir "$USER_UNIT_DIR" 0700
}

# put_file SRC DST MODE: copies when different; sets CHANGED=1 then.
CHANGED=0
put_file() {
    if [ -f "$2" ] && cmp -s "$1" "$2"; then
        info "unchanged: $2"
    else
        install -m "$3" -o "$OA_USER" -g "$OA_USER" "$1" "$2"
        info "installed: $2"
        CHANGED=1
    fi
}

install_helpers() {
    say "Installing /usr/local/sbin/oa-podman and oa-systemctl (run podman/systemctl as $OA_USER)"
    local tmp
    tmp=$(mktemp)
    cat > "$tmp" <<'EOF'
#!/bin/bash
# Run podman as the openarcanum user (rootless containers), from root.
# Example: oa-podman ps    oa-podman exec -it openarcanum-site site admin list
set -euo pipefail
uid=$(id -u openarcanum)
cd /home/openarcanum
exec runuser -u openarcanum -- env HOME=/home/openarcanum XDG_RUNTIME_DIR="/run/user/$uid" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" podman "$@"
EOF
    install -m 0755 "$tmp" /usr/local/sbin/oa-podman
    cat > "$tmp" <<'EOF'
#!/bin/bash
# Run systemctl --user for the openarcanum user, from root.
# Example: oa-systemctl status openarcanum-relay    oa-systemctl restart openarcanum-relay
set -euo pipefail
uid=$(id -u openarcanum)
cd /home/openarcanum
exec runuser -u openarcanum -- env HOME=/home/openarcanum XDG_RUNTIME_DIR="/run/user/$uid" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" systemctl --user "$@"
EOF
    install -m 0755 "$tmp" /usr/local/sbin/oa-systemctl
    rm -f "$tmp"
    info "done"
}

install_files() {
    say "Placing the config, the Quadlet file and the README"
    if [ -n "${CONFIG_NAME:-}" ]; then
        put_file "$HOST_DIR/$CONFIG_NAME" "$OA_HOME/$APP_DIR/config/$CONFIG_NAME" 0640
    fi
    # The Quadlet file as the kit has it, plus the monitoring lines (if on)
    # at the top of [Container].
    local from quadlet
    from=$(monitoring_choice)
    quadlet=$(mktemp)
    awk -v extra="$(monitoring_lines "$from")" '{ print } /^\[Container\]$/ && extra != "" { print extra }' \
        "$HOST_DIR/$SERVICE.container" > "$quadlet"
    put_file "$quadlet" "$QUADLET_DIR/$SERVICE.container" 0640
    rm -f "$quadlet"
    ensure_monitoring "$from"
    local u
    for u in ${EXTRA_UNITS:-}; do
        put_file "$HOST_DIR/$u" "$USER_UNIT_DIR/$u" 0640
    done
    install -m 0600 "$HOST_DIR/README.md" /root/README.md
    info "installed: /root/README.md"
    if command -v restorecon >/dev/null; then
        restorecon -R "$OA_HOME/.config"
    fi
}

check_quadlet() {
    say "Checking the Quadlet file with the generator"
    local gen=/usr/libexec/podman/quadlet
    if [ -x "$gen" ]; then
        local log
        log=$(mktemp)
        if ! as_oa "$gen" -dryrun -user >/dev/null 2>"$log"; then
            cat "$log" >&2
            rm -f "$log"
            die "the Quadlet generator rejected the files in $QUADLET_DIR"
        fi
        if grep -qi 'error\|unsupported' "$log"; then
            cat "$log" >&2
            rm -f "$log"
            die "the Quadlet generator reported problems (is podman too old for one of the keys?)"
        fi
        rm -f "$log"
        info "ok"
    else
        warn "$gen not found; skipping the dry run"
    fi
}

image_of() { sed -n 's/^Image=//p' "$HOST_DIR/$SERVICE.container"; }

# verify_image IMAGE: checks the keyless signature as the user and prints
# the digest it covers (empty when there is none). cosign reads docker's
# config.json for the registry login; podman's auth.json has the same
# shape, so a private package needs only the login from the README.
verify_image() {
    local auth="$OA_HOME/.config/containers/auth.json" env=()
    if [ -f "$auth" ]; then
        make_dir "$OA_HOME/.config/cosign-docker" 0700
        ln -sfn "$auth" "$OA_HOME/.config/cosign-docker/config.json"
        env=(DOCKER_CONFIG="$OA_HOME/.config/cosign-docker")
    fi
    as_oa env "${env[@]}" cosign verify --certificate-oidc-issuer "$IMAGE_OIDC_ISSUER" \
        --certificate-identity-regexp "$IMAGE_SIGNER" --output json "$1" 2>/dev/null \
        | grep -o '"docker-manifest-digest":"sha256:[0-9a-f]*"' | head -n1 | cut -d'"' -f4 || true
    # A failed check prints nothing, so pull_image explains it (with
    # pipefail and the ERR trap, failing here would only print a line number).
}

pull_image() {
    local image repo digest
    image=$(image_of)
    repo=${image%%@*}
    repo=${repo%:*}
    say "Checking the signature of $image (cosign, keyless: the Containers workflow's release tags)"
    digest=$(verify_image "$image")
    [ -n "$digest" ] || die "no valid signature for $image. The images are signed by the Containers workflow of
  github.com/hyperkubeorg/openarcanum for its release tags (docs/release.md); a tag that isn't a release, or a
  tampered image, fails here. If the package is private on ghcr.io, log in once as the user:
    oa-podman login --authfile /home/openarcanum/.config/containers/auth.json ghcr.io
  (user name: your GitHub user; password: a token with only read:packages), then run this script again.
  See 'GHCR' in deploy/podman/README.md."
    info "signed; covers $digest"
    say "Pulling $repo@$digest as $OA_USER"
    if ! oa_podman pull -q "$repo@$digest" >/dev/null; then
        die "could not pull $repo@$digest. If the package is private on ghcr.io, see 'GHCR' in deploy/podman/README.md."
    fi
    # The Quadlet file names the tag: point it at what was verified.
    case "$image" in
        *@*) ;;
        *) oa_podman tag "$repo@$digest" "$image" ;;
    esac
    info "ok: $(oa_podman image inspect --format '{{.Digest}}' "$repo@$digest")"
}

start_service() {
    say "Starting $SERVICE (systemd user unit generated from the Quadlet file)"
    oa_systemctl daemon-reload
    local t
    for t in ${EXTRA_TIMERS:-}; do
        oa_systemctl enable --now "$t" >/dev/null 2>&1 || oa_systemctl start "$t"
        info "timer $t on"
    done
    local backups
    backups=$(backup_choice)
    for t in ${BACKUP_TIMERS:-}; do
        if [ "$backups" = on ]; then
            oa_systemctl enable --now "$t" >/dev/null 2>&1 || oa_systemctl start "$t"
            info "timer $t on (backups on)"
        else
            oa_systemctl disable --now "$t" >/dev/null 2>&1 || true
            info "timer $t off (backups off; BACKUPS=on bash setup.sh turns them on)"
        fi
    done
    # Generated units can't be "enabled"; [Install] WantedBy=default.target
    # in the Quadlet file starts them at boot.
    if [ "$CHANGED" = 1 ] || ! oa_systemctl -q is-active "$SERVICE"; then
        oa_systemctl restart "$SERVICE"
        info "started"
    else
        info "already running with the same files"
    fi
}

check_active() {
    say "Check: $SERVICE is active and healthy"
    local i
    for i in $(seq 1 30); do
        oa_systemctl -q is-active "$SERVICE" && break
        sleep 2
    done
    if ! oa_systemctl -q is-active "$SERVICE"; then
        show_logs
        die "$SERVICE is not active"
    fi
    info "active"
    for i in $(seq 1 45); do
        if oa_podman healthcheck run "$SERVICE" >/dev/null 2>&1; then
            info "health check passes"
            return 0
        fi
        sleep 2
    done
    show_logs
    die "the container's health check does not pass"
}

show_logs() {
    printf '\n    Last log lines of %s:\n' "$SERVICE" >&2
    journalctl --no-pager -n 40 CONTAINER_NAME="$SERVICE" >&2 || true
    oa_systemctl --no-pager status "$SERVICE" >&2 || true
}

# check_dns NAME: the name resolves, and to an address of this machine.
check_dns() {
    say "Check: DNS for $1"
    local addrs local_addrs a found=no
    addrs=$(getent ahosts "$1" | awk '{print $1}' | sort -u || true)
    [ -n "$addrs" ] || die "$1 does not resolve. Create its A (and AAAA) record first; Let's Encrypt needs it."
    local_addrs=$(ip -o addr show scope global | awk '{sub(/\/.*/, "", $4); print $4}' || true)
    for a in $addrs; do
        if printf '%s\n' "$local_addrs" | grep -qxF "$a"; then
            info "$1 -> $a (on this machine)"
            found=yes
        else
            info "$1 -> $a (not on an interface here)"
        fi
    done
    # Read by the relay's setup.sh.
    # shellcheck disable=SC2034
    DNS_LOCAL=$found
    [ "$found" = yes ] || warn "$1 does not point at an address of this machine. Fine behind 1:1 NAT (cloud), otherwise fix DNS before ACME runs into rate limits."
}

# check_tls NAME PATH: the service answers https://NAME/PATH on this
# machine with a certificate browsers trust (waits for ACME).
check_tls() {
    say "Check: TLS certificate for $1 (Let's Encrypt, tls-alpn-01 on port 443)"
    local i
    for i in $(seq 1 36); do
        if curl -fsS -o /dev/null --max-time 10 --resolve "$1:443:127.0.0.1" "https://$1$2" 2>/dev/null; then
            info "https://$1$2 answers with a trusted certificate"
            echo | openssl s_client -connect 127.0.0.1:443 -servername "$1" 2>/dev/null \
                | openssl x509 -noout -issuer -enddate | sed 's/^/    /' || true
            return 0
        fi
        [ "$i" = 1 ] && info "waiting for the certificate (up to 3 minutes)"
        sleep 5
    done
    show_logs
    die "no trusted certificate for $1 yet. See README.md, 'Certificates'."
}

# check_public URL: the same from the outside name (hairpin may fail behind NAT).
check_public() {
    if curl -fsS -o /dev/null --max-time 10 "$1"; then
        info "$1 reachable by its public name"
    else
        warn "$1 not reachable from this machine by its public name (normal behind some NAT setups; test from another machine)"
    fi
}

setup_main() {
    preflight
    install_packages
    install_cosign
    ensure_clock
    ensure_user
    ensure_sysctl
    ensure_firewall
    disable_extras
    ensure_dirs
    install_helpers
    install_files
    check_quadlet
    if declare -F host_prechecks >/dev/null; then host_prechecks; fi
    pull_image
    if declare -F host_before_start >/dev/null; then host_before_start; fi
    start_service
    check_active
    host_checks
    say "Done. Read /root/README.md for daily operation."
}
