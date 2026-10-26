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
  `net.ipv4.ip_unprivileged_port_start = 443`. No port forwarding, so the
  servers see client addresses for their rate limits.
- Each server gets its own Let's Encrypt certificate (tls-alpn-01 on 443).
- Logs go to journald. The image's health check runs, and a failing check
  restarts the service.
- The image tag is pinned. `podman auto-update` is possible (label
  `io.containers.autoupdate=registry`); its timer is off.

Layout:

```
deploy/podman/
  lib/common.sh                     steps shared by the three setup.sh scripts
  maintainer-01.openarcanum.org/    setup.sh, Quadlet file, meta.json, backup timer, README.md
  relay-01.openarcanum.org/         setup.sh, Quadlet file, relay.json, README.md
  openarcanum.org/                  setup.sh, Quadlet file, site.json, backup timer, README.md
```

Each server's README.md is its operations manual. setup.sh copies it to
`/root/README.md` on that server.

## Before you start

- DNS A records (and AAAA, if the servers have IPv6) for the three names,
  pointing at the right servers. Let's Encrypt checks them.
- Root SSH access to each server.
- The release images on ghcr.io (see "GHCR" below). The Quadlet files pin
  `2026.10.0`; change the `Image=` lines if you deploy another release.

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
  `/run/user/<uid>`, which is gone after a reboot. podman and
  `podman auto-update` read the file above by themselves.

setup.sh stops with a hint when the pull is refused.
