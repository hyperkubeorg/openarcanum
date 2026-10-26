# OpenArcanum

OpenArcanum is chat for communities, in the style of Discord, without
accounts and without a company in the middle. You sign in with a key the app
makes on your computer, each community runs its own server (on a home machine
or a small VPS), and the shared infrastructure only passes along encrypted
traffic it can't read. Direct messages are end-to-end encrypted. There are
text channels, roles and permissions, moderation tools, and voice and video
calls with screen sharing.

How it fits together:

- **The desktop app** (Linux, Windows, macOS) keeps your keys in an encrypted
  profile on your computer. By default you get a separate key for each
  community, so communities can't link your activity.
- **A community server** holds one community: its members, channels and
  messages, encrypted on disk. It never needs an open port; it connects out
  to relays. The app can also host a community itself.
- **Relays** forward encrypted traffic and call media. They store nothing and
  are interchangeable.
- **Maintainer nodes** keep the community directory, the list of relays,
  and mailboxes that hold encrypted direct messages for people who are
  offline.

[`docs/architecture.md`](docs/architecture.md) explains the design and what
each party can and can't see.

## Quick start (Linux)

### What to install

- **Go** (the version in `go.mod`) and **uv** (it installs the Python version in
  `clients/desktop/.python-version` by itself).
- **Qt WebEngine's system libraries** for the desktop app. On Arch Linux:

  ```bash
  sudo pacman -S --needed nss nspr pcsclite alsa-lib libpulse pipewire-alsa fontconfig freetype2 \
    dbus mesa libglvnd libxkbcommon libxkbcommon-x11 libxcomposite libxdamage libxrandr libxtst \
    libxkbfile libxshmfence libxcursor libxi xcb-util xcb-util-cursor xcb-util-image \
    xcb-util-keysyms xcb-util-renderutil xcb-util-wm noto-fonts noto-fonts-emoji ttf-dejavu
  ```

  This is the list in `deploy/container/Containerfile.client`. On Debian or
  Ubuntu, the "Linux packages" step of `.github/workflows/ci.yml` has the
  equivalent `apt` list.
- For `make check`: **protoc** and **protoc-gen-go**, **golangci-lint** v2,
  **Node.js** 22 (the UI's JavaScript tests), and **Xvfb** and **dbus**
  (`xvfb-run`, `dbus-run-session`) for the headless window tests. `make audit`
  also needs `govulncheck`. CI pins each version in the `env:` block of
  `.github/workflows/ci.yml`.
- For the local Kubernetes stack (optional): rootless **podman**, **kind**,
  **kubectl** and **helm** ([`docs/kubernetes.md`](docs/kubernetes.md) "What you need").

### Run it

```bash
make dev-up                # builds bin/relay, bin/community and bin/meta, then starts a local
                           # relay, maintainer node and community server (data in .dev/)
                           # and prints a claim code; `scripts/dev-up.sh reset` starts afresh
make desktop NAME=alice    # in another terminal: the app from source, profile in .dev/desktop/alice
make desktop NAME=bob      # a second person
```

In the first window, create a profile, then choose **+ → Host on your own
server** and paste the claim code. Create an invite in the new community and
paste it into the second window to join.

Instead of `make dev-up`, `make kind-up` and `make kind-proxy` run the same
servers on a local kind cluster, and `make client-image` then
`make client NAME=alice` runs each copy of the app in its own container
([`docs/kubernetes.md`](docs/kubernetes.md)). `make desktop-appimage` runs the
AppImage build instead of the source. `make` on its own lists every target;
`make clean`, `make clean-dev`, `make clean-images` and `make clean-all` remove
what the project leaves behind.

### Checks

`make check` runs everything CI runs, and never rewrites files: formatting and
generated-code checks, Go and Python lint, lock-file pinning, the storage
guard, Go tests (with `-race`), Python unit, interop, end-to-end and UI tests,
the UI's JavaScript tests, a short fuzz run and a headless launch of the app.
`make fmt` and `make proto` fix what the formatting and generated-code checks
report. [`docs/architecture.md`](docs/architecture.md) §20 lists each step.

## Documentation

| For | Read |
|---|---|
| People using the app | [`docs/user-guide.md`](docs/user-guide.md) |
| Community owners and moderators | [`docs/admin-guide.md`](docs/admin-guide.md) |
| Running relays, maintainer nodes and community servers | [`docs/operations.md`](docs/operations.md), [`docs/kubernetes.md`](docs/kubernetes.md), [`deploy/podman/`](deploy/podman/README.md) |
| Every setting and its default | [`docs/settings.md`](docs/settings.md) |
| Design | [`docs/architecture.md`](docs/architecture.md), [`docs/voice-decision.md`](docs/voice-decision.md), [`docs/design.md`](docs/design.md) (the app's visual design) |
| Security review | [`docs/threat-model.md`](docs/threat-model.md), [`docs/protocol.md`](docs/protocol.md), [`docs/crypto-inventory.md`](docs/crypto-inventory.md), [`docs/audit-package.md`](docs/audit-package.md) |
| Testing by hand | [`docs/QA_GUIDE.md`](docs/QA_GUIDE.md), [`docs/manual-tests/`](docs/manual-tests/) |
| Installers and releases | [`docs/packaging.md`](docs/packaging.md) (`make bundle`, `make appimage`), [`docs/release.md`](docs/release.md) |
| Translating the app | [`docs/translations.md`](docs/translations.md) |

## Layout

`cmd/` node binaries · `internal/` Go packages · `proto/` wire schemas ·
`clients/desktop/` the desktop app (a uv project) · `clients/shared/ui/` the
app's UI · `deploy/` container, Helm, kind, podman and systemd files ·
`scripts/` build and check helpers · `docs/` documentation.

## Licence

[AGPL-3.0-only](LICENSE). Bundled and vendored components and their licences
are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
Source: https://github.com/hyperkubeorg/openarcanum
