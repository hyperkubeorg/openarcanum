# Production servers with rootless podman

This directory sets up the first three OpenArcanum servers on Rocky Linux 9 or
later, each with one container under rootless podman:

| Server | Service | Image | Ports |
|---|---|---|---|
| `maintainer-01.openarcanum.org` | maintainer node (`meta`) | `ghcr.io/hyperkubeorg/openarcanum-maintainer` | 443/tcp |
| `relay-01.openarcanum.org` | relay (`relay`) | `ghcr.io/hyperkubeorg/openarcanum-relay` | 443/tcp, 7000/udp |
| `openarcanum.org` | website (`site`) | `ghcr.io/hyperkubeorg/openarcanum-site` | 443/tcp, 80/tcp (redirect) |

On every server:

- The container runs as the unprivileged user `openarcanum`, from a Quadlet
  file in `/home/openarcanum/.config/containers/systemd/`. Lingering is on, so
  it starts at boot.
- Data lives in plain directories under `/home/openarcanum/<service>/`,
  bind-mounted into the container with `:Z` SELinux labels. The image's uid
  65532 is mapped to `openarcanum` (`UserNS=keep-id:uid=65532,gid=65532`), so
  the files belong to `openarcanum` on the host.
- The container uses the host network and binds 443 itself, allowed by
  `net.ipv4.ip_unprivileged_port_start = 443` (see "Port 443" below). No
  port forwarding, so the servers see client addresses for their rate
  limits.
- Each server gets its own Let's Encrypt certificate (tls-alpn-01 on 443).
- Logs go to journald. The image's health check runs, and a failing check
  restarts the service.
- The image tag is pinned, and setup.sh takes it only with a valid keyless
  cosign signature from the repository's Containers workflow for a release
  tag (docs/release.md "Container images"); it then pulls the digest that
  signature covers. Once the first release is out, pin its digest in the
  Quadlet file as well (`Image=...:<version>@sha256:...`). `podman
  auto-update` is possible (label `io.containers.autoupdate=registry`) but
  pulls without that check; its timer is off.
- Scheduled backups (maintainer node, website) are off until turned on with
  `BACKUPS=on bash setup.sh` (each README, "Backups and restore"); once on,
  they stay on the machine until the backup copy unit is set up ("Copying
  backups to another machine").

Layout:

```
deploy/podman/
  lib/common.sh                     steps shared by the three setup.sh scripts
  maintainer-01.openarcanum.org/    setup.sh, Quadlet file, meta.json, backup timer and copy, README.md
  relay-01.openarcanum.org/         setup.sh, Quadlet file, relay.json, README.md
  openarcanum.org/                  setup.sh, Quadlet file, site.json, backup timer and copy, README.md
```

Each server's README.md is its operations manual. setup.sh copies it to
`/root/README.md` on that server.

## Before you start

- DNS A records (and AAAA, if the servers have IPv6) for the three names,
  pointing at the right servers. Let's Encrypt checks them.
- Root SSH access to each server.
- The release images on ghcr.io (see "GHCR" below). The Quadlet files pin
  `2026.10.18` (maintainer, site and relay); change the `Image=` lines if you deploy another release.
  Only a release tag works: setup.sh checks the image's signature.

## Order of setup

Copy the whole `deploy/podman` directory to each server, because the scripts
use `lib/common.sh`. Then run that server's script as root.

1. **Maintainer node first.** Relays register with it.

   ```sh
   scp -r deploy/podman root@maintainer-01.openarcanum.org:/root/openarcanum-podman
   ssh root@maintainer-01.openarcanum.org bash /root/openarcanum-podman/maintainer-01.openarcanum.org/setup.sh
   ```

2. **Relay second.** Its script ends by checking that the maintainer lists it.

   ```sh
   scp -r deploy/podman root@relay-01.openarcanum.org:/root/openarcanum-podman
   ssh root@relay-01.openarcanum.org bash /root/openarcanum-podman/relay-01.openarcanum.org/setup.sh
   ```

3. **Website last.** Then create the first admin, as the script tells you:

   ```sh
   scp -r deploy/podman root@openarcanum.org:/root/openarcanum-podman
   ssh root@openarcanum.org bash /root/openarcanum-podman/openarcanum.org/setup.sh
   ssh -t root@openarcanum.org oa-podman exec -it openarcanum-site site admin add NAME
   ssh -t root@openarcanum.org oa-podman exec -it openarcanum-site site admin totp NAME
   ```

The scripts can run again at any time. They change only what differs and
restart the service only when its files changed. The copy in
`/root/openarcanum-podman` on each server is where config changes and image
upgrades are made (see each README).

## GHCR

podman pulls the images from ghcr.io as the user `openarcanum`. Either:

- make the four packages (`openarcanum-relay`, `openarcanum-maintainer`,
  `openarcanum-site`, `openarcanum-community`) public: on GitHub, the
  organization's Packages tab, each package, Package settings, Change
  visibility, Public. Nothing else is needed then. Or
- keep them private and log in once on each server with a token that can only
  read packages (a classic personal access token with just `read:packages`, or
  a fine-grained one with read access to packages):

  ```sh
  oa-podman login --authfile /home/openarcanum/.config/containers/auth.json ghcr.io
  ```

  User name: your GitHub user. Password: the token (typed at the prompt, not
  on the command line). Without `--authfile`, podman keeps the login in
  `/run/user/<uid>`, which is gone after a reboot. podman,
  `podman auto-update` and the signature check (cosign, through a link
  setup.sh makes) read the file above by themselves.

setup.sh stops with a hint when the signature check or the pull is refused.

## Port 443

The servers bind 443 themselves, so `net.ipv4.ip_unprivileged_port_start =
443` is set on every host. That lets any unprivileged process on the host
bind 443 and up; `openarcanum` is the only such user there (no password, no
sudo), and nothing else runs on these machines. The alternative, listening
on a high port and forwarding 443 to it with firewalld (as the website does
for port 80), was not taken: a forward applies only to traffic arriving
from outside, so the host's own checks (setup.sh, the health checks, `curl
--resolve`) would go to another port than clients do, and firewalld's
forward-ports are IPv4 only, so IPv6 would need a rich rule per host. Client
addresses would stay visible either way (a forward rewrites the destination
port, not the source).
