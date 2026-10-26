# Shared steps for the setup.sh scripts in deploy/podman/*/.
# Sourced, not run. Every function prints what it does and stops the
# script on the first error (set -Eeuo pipefail in the caller).
#
# The caller sets, before calling setup_main:
#   SERVICE      systemd unit and container name, e.g. openarcanum-relay
#   APP_DIR      the service's directory under /home/openarcanum, e.g. relay
#   CONFIG_NAME  the JSON config file name, e.g. relay.json
#   DATA_DIRS    subdirectories of APP_DIR the container writes (data keys acme ...)
#   TCP_PORTS    ports to open in firewalld, e.g. "443/tcp"
#   EXTRA_UNITS  optional extra user units (backup .service/.timer) in HOST_DIR
#   EXTRA_TIMERS optional timers to enable
# and defines host_checks (run after the service is up).

OA_USER=openarcanum
OA_HOME=/home/$OA_USER
QUADLET_DIR=$OA_HOME/.config/containers/systemd
USER_UNIT_DIR=$OA_HOME/.config/systemd/user
SYSCTL_FILE=/etc/sysctl.d/90-openarcanum.conf
MIN_PODMAN=4.9

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
    say "Installing packages (podman, firewalld, chrony, curl, openssl)"
    dnf -y -q install podman container-selinux firewalld chrony curl openssl policycoreutils
    local v
    v=$(podman version --format '{{.Client.Version}}' 2>/dev/null || podman --version | awk '{print $3}')
    info "podman $v"
    version_ge "$v" "$MIN_PODMAN" || die "podman $v is too old (need $MIN_PODMAN or newer for these Quadlet files)"
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
    firewall-cmd -q --reload
    info "zone $(firewall-cmd --get-default-zone): ports $(firewall-cmd --list-ports)"
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
    put_file "$HOST_DIR/$CONFIG_NAME" "$OA_HOME/$APP_DIR/config/$CONFIG_NAME" 0640
    put_file "$HOST_DIR/$SERVICE.container" "$QUADLET_DIR/$SERVICE.container" 0640
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

pull_image() {
    local image
    image=$(image_of)
    say "Pulling $image as $OA_USER"
    if ! oa_podman pull -q "$image" >/dev/null; then
        die "could not pull $image. If the package is private on ghcr.io, log in once as the user:
    oa-podman login --authfile /home/openarcanum/.config/containers/auth.json ghcr.io
  (user name: your GitHub user; password: a token with only read:packages), then run this script again.
  See 'GHCR' in deploy/podman/README.md."
    fi
    info "ok: $(oa_podman image inspect --format '{{.Digest}}' "$image")"
}

start_service() {
    say "Starting $SERVICE (systemd user unit generated from the Quadlet file)"
    oa_systemctl daemon-reload
    local t
    for t in ${EXTRA_TIMERS:-}; do
        oa_systemctl enable --now "$t" >/dev/null 2>&1 || oa_systemctl start "$t"
        info "timer $t on"
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
    ensure_clock
    ensure_user
    ensure_sysctl
    ensure_firewall
    ensure_dirs
    install_helpers
    install_files
    check_quadlet
    if declare -F host_prechecks >/dev/null; then host_prechecks; fi
    pull_image
    start_service
    check_active
    host_checks
    say "Done. Read /root/README.md for daily operation."
}
