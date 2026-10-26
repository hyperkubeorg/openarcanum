# Releases and auto-update

How a release is made (a version tag → GitHub Actions → a draft GitHub
Release → a maintainer publishes it), what the repository owner configures
for it, what testers do to install each build, and how the signed update
manifest and the client's updater work. How each installer is built and
signed is in `docs/packaging.md`; risks are in `docs/threat-model.md`
"Updates".

The repository is https://github.com/hyperkubeorg/openarcanum. Release
files, images and URLs use the name "openarcanum".

## Versions

Calendar versioning: **`YYYY.MM.PATCH`**, with **`-beta.N`** for a beta of
that version. The first beta is `2026.10.0`; a fix in the same month is
`2026.10.1`; the first release of November is `2026.11.0`, and a beta of it
before then `2026.11.0-beta.1`. The month has no leading zero (`2027.1.0`):
Semantic Versioning, which the updater and the container tags compare by,
forbids leading zeros, and every version here is a valid Semantic Version
(`2026.10.0-beta.1` < `2026.10.0` < `2026.10.1` < `2026.11.0`). The git tag
is `v<version>` (`v2026.10.0`); pushing it is what starts a release.

`VERSION` at the repository root is the single source of truth. Everything
else derives from it:

| Where | What | How |
|---|---|---|
| Go binaries (`relay`, `meta`, `community`, the bundled community server) | `internal/version.Release` and `.Revision`; `relay version` prints them | `-ldflags -X`, from the Makefile (`make build`, `release-servers`, `bundle-server`) and the Containerfile's `VERSION`/`REVISION` build arguments; a plain `go build` says `dev` |
| The desktop app | `openarcanum.__version__` (Settings → About, the updater's "newer than" check) | copy written by `scripts/version` |
| Python packaging | `clients/desktop/pyproject.toml` and the `openarcanum` entry in `uv.lock`, as PEP 440 (`2026.11.0b1` for `2026.11.0-beta.1`) | copy written by `scripts/version` |
| macOS bundle, Windows executable and installer | `CFBundleShortVersionString`/`CFBundleVersion`, `VersionInfoVersion`: numbers only (`2026.11.0`); the full version in `OpenArcanumReleaseVersion`, `ProductVersion` and the installer's name | `packaging/openarcanum.spec`, `packaging/windows/openarcanum.iss` |
| Helm chart | `version` and `appVersion` (the default image tag) | copy written by `scripts/version` |
| Container images | tags `<version>`, `YYYY.MM`, `latest`, `sha-<commit>`; label `org.opencontainers.image.version` | `.github/workflows/containers.yml` |
| Release files and the update manifest | `openarcanum-<version>-…`, manifest `version` | `.github/workflows/release.yml` |

`make version` prints the version and checks every copy agrees;
`make version-check` (part of `make check`) fails when one doesn't. Both
release workflows refuse a tag that isn't `v<VERSION>`.

### Bumping and tagging

`scripts/version` (Python standard library; `uv run --project
clients/desktop python scripts/version …` where `python3` isn't on PATH)
never pushes:

```sh
scripts/version                     # the version (VERSION)
scripts/version bump next           # this month's next: 2026.10.0 -> 2026.10.1, or 2026.11.0 in a new month
scripts/version bump patch          # 2026.10.0 -> 2026.10.1
scripts/version bump month          # -> <this year>.<this month>.0
scripts/version bump month --beta   # -> <this year>.<this month>.0-beta.1
scripts/version bump beta           # 2026.11.0-beta.1 -> 2026.11.0-beta.2
scripts/version bump final          # 2026.11.0-beta.2 -> 2026.11.0
scripts/version set 2026.10.0       # any version, explicitly
scripts/version bump next --commit  # also commit the version files ("Version X")
scripts/version bump next --tag     # commit, then make the annotated tag vX
scripts/version tag                 # tag the current VERSION on HEAD (clean tree)
```

Bumping, committing and tagging need a clean working tree; each prints the
`git push` command to run next.

## Making a release

From an up-to-date checkout of `main` with nothing uncommitted:

```sh
git switch main && git pull --ff-only
make version                        # what will be released, and the copies agree
scripts/version bump next --tag     # or: bump month --beta --tag, bump final --tag, ...
#   for the first beta, VERSION is already 2026.10.0:  scripts/version tag
git push origin main v2026.10.0     # the branch, then the tag: this starts both workflows
```

The tag starts two workflows (Actions tab):

**Release** (`.github/workflows/release.yml`):

| Job | Runner | Result |
|---|---|---|
| version | ubuntu | the tag equals `v<VERSION>`; beta or final |
| linux | ubuntu-22.04 | `openarcanum-<v>-linux-x86_64.AppImage` and `.tar.gz` (the bundle and the AppImage smoke-run first) |
| windows | windows-latest | `openarcanum-<v>-windows-x86_64-setup.exe` (installed, smoke-run, upgraded, uninstalled first; Authenticode-signed with the certificate) |
| macos | macos-latest (Apple silicon) | `openarcanum-<v>-macos-arm64.dmg` (checked and smoke-run from the image first; signed and notarized with the Apple credentials) |
| servers | ubuntu | `openarcanum-servers-<v>-linux-amd64.tar.gz`, `-linux-arm64.tar.gz` (static binaries, run natively and under qemu, version-checked) |
| manifest | ubuntu, environment `release` | `manifest.json`, the signed update manifest (waits for a reviewer's approval; skipped without the key, see below) |
| release | ubuntu | a **draft** GitHub Release for the tag with every file above, `SHA256SUMS`, and `SHA256SUMS.sigstore.json` (a keyless Sigstore signature of the checksums), notes from `.github/release-notes.md` (how to install, how to verify, the checksums), marked pre-release for a `-beta.N` |

**Containers** (`.github/workflows/containers.yml`): the images, pushed and
signed (below).

Then, on GitHub → Releases:

1. Open the draft `v<version>`. Read the notes; edit them if you like
   (what changed).
2. Download and try the files you can (the notes say how to install
   each); compare with `SHA256SUMS`.
3. Click **Publish release**. Only now are the files public, and only now
   does `https://github.com/hyperkubeorg/openarcanum/releases/latest/download/manifest.json`
   point at this release's manifest (GitHub's "latest" skips drafts and
   pre-releases, so a beta's manifest is not offered there; release the
   beta as `YYYY.MM.PATCH` without `-beta.N` when installed copies should be
   offered it).

If a job fails, fix the cause on `main`, delete the tag and any draft made
for it (`gh release delete v2026.10.0`, `git push origin
:refs/tags/v2026.10.0`, `git tag -d v2026.10.0`), and tag again; or, when the fix needs no code change (a secret, an outage), re-run
the failed jobs, or run the workflow by hand from the tag (Actions →
Release → Run workflow → the tag): a re-run replaces the files of the
existing draft and never touches a published release.

Run by hand from a branch, either workflow builds everything without
releasing anything: Release keeps the files as workflow artifacts (no
manifest, no GitHub Release), Containers pushes only `sha-<commit>` tags.
Pull requests run Containers without pushing; `ci.yml` builds the desktop
bundles and installers on every push and pull request.

## What the repository owner configures

None of it is needed for a first build: without the secrets everything
still builds, unsigned, with checksums (next section).

| What | Where (GitHub → Settings) | Used by |
|---|---|---|
| Nothing for ghcr.io: the workflow pushes with its own `GITHUB_TOKEN` (`packages: write`) | Make the packages public once they exist (below) | containers.yml |
| `APPLE_DEVELOPER_ID_P12`: the "Developer ID Application" certificate and its private key, exported from Keychain Access as `.p12`, base64 (`base64 -i id.p12 \| gh secret set APPLE_DEVELOPER_ID_P12`); `APPLE_DEVELOPER_ID_PASSWORD`: the `.p12`'s password | Secrets and variables → Actions → Repository secrets | release.yml macos: `codesign` with the hardened runtime |
| `APPLE_ID` (the Apple Account email), `APPLE_TEAM_ID` (10 characters, developer.apple.com → Membership), `APPLE_APP_PASSWORD` (an app-specific password from account.apple.com) | Repository secrets | release.yml macos: `notarytool` + `stapler` (needs the certificate too) |
| `WINDOWS_CERT_PFX`: an Authenticode code-signing certificate with its key as `.pfx`, base64 (`base64 -w0 cert.pfx \| gh secret set WINDOWS_CERT_PFX`); `WINDOWS_CERT_PASSWORD` | Repository secrets | release.yml windows: `signtool` on the app, the installer and the uninstaller |
| The `release` environment with **required reviewers** (yourself and whoever may approve releases) and a deployment rule allowing only tags `v*` | Environments → New environment `release` | release.yml manifest (waits for approval) |
| `RELEASE_SIGNING_KEY`: the Ed25519 release key, PEM ("The release key" below): `gh secret set RELEASE_SIGNING_KEY --env release < release.pem` | Environments → `release` → Environment secrets (never a repository secret) | release.yml manifest |
| Optional variables `RELEASE_BASE_URL` (serve the files from another host: `<url>/<version>/<file>`; default: the GitHub Release) and `RELEASE_MANIFEST_DAYS` (default 30) | Secrets and variables → Actions → Variables | release.yml manifest |

**The update key goes in with the code.** Installed copies only accept a
manifest signed by a key built into them, so before adding
`RELEASE_SIGNING_KEY`, commit its public key to `RELEASE_PUBLIC_KEYS` in
`clients/desktop/src/openarcanum/updates.py` (`MANIFEST_URL` already is
`https://github.com/hyperkubeorg/openarcanum/releases/latest/download/manifest.json`;
update the `RELEASE_PUBLIC_KEYS is None` assertion of `test_config_and_test_override`
in `clients/desktop/tests/test_updates.py`), in the release being tagged. The
manifest job checks the manifest the way that release's app will
(`release_manifest.py verify --builtin`) and fails, so no draft is made,
when the secret is set but the app doesn't trust its key. A release
without the key in its code can't update itself later either: testers
install the next version by hand.

**Installer publisher and bundle id.** The Windows installer's publisher
is `AppPublisher` in `clients/desktop/packaging/windows/openarcanum.iss`
(now the product name); set it to the name on the code-signing
certificate. The macOS bundle id is `BUNDLE_ID` in
`clients/desktop/packaging/openarcanum.spec` (`org.openarcanum.OpenArcanum`).
Don't change it after the first signed release: macOS ties permissions and
the keychain entry to it.

**Container packages.** The first push creates
`openarcanum-relay`, `-maintainer`, `-community` (and `-site`) under the
hyperkubeorg organization, private. For each: github.com/orgs/hyperkubeorg/packages
→ the package → Package settings → Change visibility → **Public** (the
organization must allow public packages: Organization settings → Packages).
They are linked to the repository through their
`org.opencontainers.image.source` label. If a package was ever created
by hand, give the repository write access in that package's "Manage
Actions access".

## Repository settings

Suggested settings on GitHub (Settings), once the repository exists:

- **Branch ruleset for `main`**: require a pull request (with approvals
  once there is more than one maintainer, and stale approvals dismissed),
  require the status checks to pass (the `make check` jobs on the three
  OSes, the server binaries and images job, the three bundle and
  installer jobs, `audit`, and the `openarcanum-*` Containers jobs),
  require branches to be up to date and linear history, and block force
  pushes and deletion.
- **Tag ruleset for `v*`**: only maintainers may create, update or delete
  these tags, since pushing one starts a release.
- **Environment `release`**: required reviewers, deployments limited to
  `v*` tags. It holds `RELEASE_SIGNING_KEY` ("The release key").
- **Actions** (Actions → General): workflow permissions "Read repository
  contents" (each workflow asks for more where it needs it); require
  approval for workflows from forks; allow only GitHub's actions, verified
  creators and the actions the workflows pin by SHA.
- **Code security**: Dependabot alerts, secret scanning with push
  protection, and private vulnerability reporting.
- **Packages**: after the first Containers run, make
  `openarcanum-relay`, `-maintainer`, `-community` and `-site` public
  ("Container packages" above).

## Without the secrets

Every job still runs and the draft release is still made; each skipped
signature is a notice in the workflow log:

| Missing | Result | What testers do |
|---|---|---|
| Windows certificate | The installer, uninstaller and app are unsigned | SmartScreen: "Windows protected your PC" → **More info** → **Run anyway**. Some antivirus products quarantine unsigned installers; restore it or add an exception |
| Apple certificate | The app and `.dmg` are ad-hoc signed and not notarized | Open the `.dmg`, drag the app to Applications, open it once; macOS refuses ("Apple could not verify…"). Then System Settings → Privacy & Security → **Open Anyway** (on macOS 14 also Control-click the app → Open → Open). Or in Terminal: `xattr -dr com.apple.quarantine /Applications/OpenArcanum.app` |
| Apple notarization credentials (with the certificate) | Signed but not notarized: Gatekeeper still blocks the first start | The same as above |
| `RELEASE_SIGNING_KEY` | No `manifest.json`; the notes say so | Installed copies don't see the release (they never contact the update host unless their build has a release key). Install the new version over the old one by hand |

Linux AppImages are not signed either way. Checksums (`SHA256SUMS`) and
their Sigstore signature are always there.

## Installing a build (testers)

| System | File | Steps |
|---|---|---|
| Windows 10/11, x64 | `openarcanum-<v>-windows-x86_64-setup.exe` | Run it. Per-user install (no administrator rights) into `%LOCALAPPDATA%\Programs\OpenArcanum`, Start menu shortcut, optional desktop shortcut, uninstaller in Settings → Apps. A newer installer upgrades in place |
| macOS 14 or later, Apple silicon | `openarcanum-<v>-macos-arm64.dmg` | Open it, drag the app to Applications. Intel Macs are not supported (`docs/packaging.md` "macOS architecture") |
| Linux, x86-64, glibc 2.35+ (Ubuntu 22.04, Debian 12, Fedora 36 and later) | `openarcanum-<v>-linux-x86_64.AppImage` | `chmod +x openarcanum-*.AppImage && ./openarcanum-*.AppImage`. Without FUSE: add `--appimage-extract-and-run`. Desktop menu entry and `openarcanum://` links: AppImageLauncher or Gear Lever (`docs/packaging.md`). Or unpack the `.tar.gz` and run `OpenArcanum` in it |

Nothing else is needed: the Python runtime, Qt and the community server
are inside each build.

## Verifying a download

```sh
sha256sum --check --ignore-missing SHA256SUMS          # macOS: shasum -a 256 --check --ignore-missing SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/hyperkubeorg/openarcanum/.github/workflows/release.yml@refs/tags/v2026.10.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

The Sigstore signature says the checksums were made by this repository's
release workflow for that tag; the Windows and macOS signatures (when
configured) say who published the installers; the update manifest
(below) is what installed copies check.

## Container images

`.github/workflows/containers.yml`, from `deploy/container/Containerfile`:

| Image | Target |
|---|---|
| `ghcr.io/hyperkubeorg/openarcanum-relay` | `relay` |
| `ghcr.io/hyperkubeorg/openarcanum-maintainer` | `meta` |
| `ghcr.io/hyperkubeorg/openarcanum-community` | `community` |
| `ghcr.io/hyperkubeorg/openarcanum-site` | `site` (the project website, `cmd/site`) |

For `linux/amd64` and `linux/arm64`. Tags for a release: `<version>`,
`sha-<commit>`, and for a final release also `YYYY.MM` (the month's newest)
and `latest`; betas move neither. Labels and index annotations:
`org.opencontainers.image.source`, `version`, `revision`, `created`,
`licenses=AGPL-3.0-only`. Each image has an SBOM and SLSA provenance
attestation (BuildKit) and a keyless cosign signature:

```sh
cosign verify ghcr.io/hyperkubeorg/openarcanum-relay:2026.10.0 \
  --certificate-identity "https://github.com/hyperkubeorg/openarcanum/.github/workflows/containers.yml@refs/tags/v2026.10.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
docker buildx imagetools inspect ghcr.io/hyperkubeorg/openarcanum-relay:2026.10.0 --format '{{ json .SBOM }}'
```

The Helm chart uses these images at its `appVersion` by default
(`docs/kubernetes.md`).

## The update manifest

One file, `manifest.json`, attached to each GitHub Release by the release
workflow and fetched by installed copies from `MANIFEST_URL` in
`clients/desktop/src/openarcanum/updates.py`
(`https://github.com/hyperkubeorg/openarcanum/releases/latest/download/manifest.json`).
It is an envelope around the signed bytes:

```json
{
  "payload": "<unpadded base64url of the canonical payload bytes>",
  "signatures": [
    {"key": "<unpadded base64url of a 32-byte Ed25519 public key>",
     "sig": "<unpadded base64url of the 64-byte signature>"}
  ]
}
```

The **payload** is JSON written canonically (sorted keys, no whitespace,
ASCII only: `json.dumps(sort_keys=True, separators=(",", ":"),
ensure_ascii=True)`). The client verifies the signature over the exact bytes
in the envelope and only then parses them, so it never depends on
re-serializing JSON the same way:

```json
{
  "product": "OpenArcanum",
  "version": "2026.11.0",
  "released_at": "2026-11-01T12:00:00Z",
  "expires_at": "2026-12-01T12:00:00Z",
  "assets": {
    "linux-x86_64":    {"url": "https://…/openarcanum-2026.11.0-linux-x86_64.AppImage",     "sha256": "<64 lowercase hex>", "size": 123456789, "kind": "appimage"},
    "windows-x86_64":  {"url": "https://…/openarcanum-2026.11.0-windows-x86_64-setup.exe", "sha256": "…", "size": 98765432, "kind": "exe"},
    "macos-universal": {"url": "https://…/openarcanum-2026.11.0-macos-arm64.dmg",          "sha256": "…", "size": 87654321, "kind": "dmg"}
  }
}
```

| Field | Rule |
|---|---|
| `product` | Exactly the client's `PRODUCT_NAME` |
| `version` | Semantic Versioning 2.0.0; precedence decides "newer" (build metadata is ignored). Releases use the CalVer form above, which is valid SemVer |
| `released_at`, `expires_at` | UTC, `YYYY-MM-DDTHH:MM:SSZ`; `expires_at` after `released_at`, and at most 30 days after it. The client refuses the manifest from `expires_at` on, and any manifest valid for longer |
| `assets` | Per platform. Known platforms: `linux-x86_64` (kind `appimage`), `windows-x86_64` (kind `exe`, the Inno Setup installer), `macos-universal` (kind `dmg`; the release `.dmg` is Apple silicon only, and the key keeps its name so the updater needs no change). Unknown platform names are ignored, so later manifests can add some |
| `url` | `https://` (plain `http://` only to this computer, and only for the test source below) |
| `sha256`, `size` | Of the exact file; `size` is 1 byte to 1 GiB |

**Signature.** Ed25519 over `"openarcanum/release-manifest/v1" || 0x00 ||
payload`, the same domain-separation rule as every other OpenArcanum signature
(`docs/protocol.md` §1). The client accepts the manifest when at least one
signature is by a trusted release key and verifies; a signature by an
untrusted key is ignored. At most 8 signatures; the file is at most 64 KiB.

## The release key

- **Key file:** an Ed25519 private key in unencrypted PKCS#8 PEM
  (`-----BEGIN PRIVATE KEY-----`, the same as `openssl genpkey -algorithm
  ed25519`), created with mode 0600.
- **Public key:** 32 raw bytes as unpadded base64url, printed by `keygen`.
  The client's trusted keys are `RELEASE_PUBLIC_KEYS` in
  `clients/desktop/src/openarcanum/updates.py`.
- **Storage:** generate the production key offline. Either keep it offline
  and sign each manifest by hand ("Signing a manifest by hand"), or store it as the
  secret `RELEASE_SIGNING_KEY` of the GitHub Environment **`release`**, never
  as a repository secret. Configure that environment with **required
  reviewers** (and, if wanted, a deployment rule for `v*` tags): the release
  workflow's `manifest` job waits for a reviewer's approval before it can
  read the key, so pushing a tag alone can't get a manifest signed. Inside
  the job only the signing step has the key in its environment; the client
  environment is installed before that step (from the hash-pinned `uv.lock`)
  and the step runs it without syncing; every action in `release.yml` is
  pinned by commit SHA. The next step checks the manifest with the client's
  own verification against the keys built into that version
  (`release_manifest.py verify --builtin`) and fails the release if
  installed copies would refuse it. Never commit a private key: the
  repository holds only public keys, and tests generate throwaway keys at
  run time.

### Key rotation

1. Generate the new key; add its public key to `RELEASE_PUBLIC_KEYS` next to
   the old one, and ship that client.
2. Sign manifests with both keys (`--key OLD --key NEW`) while clients that
   know only the old key are still in use.
3. Ship a client that lists only the new key; sign with the new key alone.
   A manifest signed only by a retired key is then refused
   (`test_updates.py::test_key_rotation`).

If a release key leaks, ship a client without it as soon as possible; until
users have that client, the leaked key can sign updates they would accept
(see the threat model).

## The release tool

`clients/desktop/scripts/release_manifest.py`, run with the client's environment
(`uv run --project clients/desktop python clients/desktop/scripts/release_manifest.py …`):

```
release_manifest.py keygen --out KEYFILE
    Writes a new key (refuses to overwrite) and prints its public key.

release_manifest.py sign --key KEYFILE [--key KEYFILE] --version V \
    --asset PLATFORM=PATH=URL [--asset ...] --expires-days N --out manifest.json
    Hashes each PATH (sha256 and size) and signs a manifest released now
    and expiring in N days (1-30). URLs must be https://.

release_manifest.py verify --pubkey=KEY [--pubkey=KEY] manifest.json   # "=" because a key can start with "-"
release_manifest.py verify --builtin manifest.json
    Checks a manifest exactly as the client does (KEY: a public key, or a
    file holding one; --builtin: the keys built into this checkout's
    client). Exit status 1 and "REFUSED: …" when it doesn't pass.
```

It uses the client's own code (`openarcanum.updates`), so what it signs is what
the client verifies. Pick `--expires-days` with the release cadence in mind:
an expired manifest stops every client from updating, and a long expiry lets
someone replaying an old manifest hide newer releases for longer. 30 days is
the most the tool signs and the client accepts (`expires_at - released_at`);
re-sign the current release before it expires.

## What the client does

**Configured or not.** A build is configured when `RELEASE_PUBLIC_KEYS` is
set and `MANIFEST_URL` isn't under `.invalid`. `MANIFEST_URL` is set;
`RELEASE_PUBLIC_KEYS` is `None` until the release key exists ("The update
key goes in with the code" above). Until then Settings → Updates says "Updates aren't
configured for this build." and the updater makes no network request at all
(`test_unconfigured_build_makes_no_request`).

**When it checks.** At startup and every 24 hours while "Check for updates
automatically" (Settings → Updates; a preference, default on) is on, and
whenever the user clicks "Check now". A check happens before unlock too:
it needs nothing from the profile. Each check is one HTTPS request to the
release host, which sees the computer's IP address and the time; the request
carries no identifier (User-Agent `OpenArcanum-updater`, no cookies). Turning
automatic checks off stops all of them.

**What it refuses** (each with a message in Settings → Updates; tests in
`clients/desktop/tests/test_updates.py`):

| Refusal | Why |
|---|---|
| No signature, a bad signature, or only signatures by keys this build doesn't trust | Only the release key holder publishes updates |
| Wrong `product` | A manifest for another product signed by the same key |
| Expired (`now >= expires_at`) | Freeze attacks: replaying an old manifest forever to hide newer releases |
| `released_at` older than a manifest already accepted on this device (remembered in `config/updates.json`) | Rollback of the manifest itself within its expiry |
| A version not newer than the running one (the same version is "up to date"; an older one is refused as a downgrade) | Rollback to a vulnerable version |
| No asset for this platform; a malformed asset (kind, hash, size) | |
| A non-https manifest, download or redirect URL | Network attackers; downgrade to plain HTTP |
| A download larger than `size` (stopped at the limit), shorter, or with another SHA-256 | A compromised host or mirror serving other bytes |
| An installed file that no longer matches (changed in the cache after the check) | The install copies the file again and hashes exactly what it writes |

**Downloads.** To `<cache>/updates/OpenArcanum-<version>-<platform>.<ext>`,
through a `.part` file. An interrupted download resumes with an HTTP range
request at the next check; a server without range support restarts it. The
file is renamed into place only after its size and SHA-256 match. Downloads
of other versions are deleted.

**Consent.** When a verified download is ready, the app asks: "Version X is
ready" with **Restart and install**, **Later** (ask again at the next check)
and **Skip this version** (automatic checks don't offer it again; "Check
now" still does). Closing the prompt is "Later". The prompt shows only over
the unlocked app, and Python refuses the install call while the profile is
locked. The call also refuses unless that exact version was downloaded and
verified and it carries the one-time token issued with the prompt, and the
page only sends it from a real click (`event.isTrusted`). The token and the
click check are page-side: script running in the page could read the token
(`update_status` returns it) and make the call, so they keep stray calls
out, not page script (`docs/threat-model.md`, "Updates", residual risks).
The copy runs in a worker thread; if it fails or takes longer than
`INSTALL_TIMEOUT` (600 s) nothing is left pending, so the update is never
started later without a new "Restart and install". Tests:
`test_happy_path_needs_consent`, `test_no_install_without_consent_over_time`,
`test_install_runs_off_the_loop_and_a_timeout_leaves_nothing_pending`,
`test_ui_updates.py` (including the refusal while locked).

**Install** (after the user's click; the app then shuts down normally, so
everything is saved, and starts the new version last, after removing its
own crash marker, so the new process doesn't report a crash):

| Platform | How this copy is detected | Install | Restart |
|---|---|---|---|
| Linux AppImage | A frozen build whose executable lies inside `$APPDIR`, with `$APPIMAGE` naming a file in a writable folder (the runtime exports both to child processes too, so an `$APPIMAGE` inherited from another AppImage, such as an editor's terminal, is not taken as this copy's) | Copy the verified file next to it (hashing what is written), fsync, `chmod 755`, rename over `$APPIMAGE`, fsync the folder | Run the new `$APPIMAGE` with the same options (`--home`, `--maintainer`, `--verbose`; never an invite link the app was opened with) |
| Windows (Inno Setup) | A frozen build with `unins000.exe` next to the executable | Copy the verified installer into a new private temp folder (hashing what is written) | Run it with `/SILENT /SUPPRESSMSGBOXES /NORESTART` as the app exits; the installer upgrades in place and restarts the app |
| macOS | A frozen build inside `OpenArcanum.app` | Copy the verified `.dmg` into a private temp folder (hashing), mount it read-only (`hdiutil attach -readonly -nobrowse`), copy its `OpenArcanum.app` next to the running bundle (`ditto`), rename the old bundle aside, rename the new one in (the old one goes back if that fails), delete the old one, unmount | `open -n OpenArcanum.app --args …` (the same options, as on Linux) |

A copy that can't update itself (run from source, a distribution package, an
AppImage in a read-only folder) still checks and says "Version X is
available" with the reason; it downloads nothing.

**Test source.** `OPENARCANUM_UPDATE_TEST_KEYS` (comma-separated public keys)
and `OPENARCANUM_UPDATE_TEST_URL` replace the built-in keys and URL, only when
both are set and the URL's host is this computer (`127.0.0.1`, `localhost`,
`::1`). Settings → Updates then says "Using a test update source on this
computer". These come only from the environment: no setting and no page
script can change the release keys or the manifest URL. The UI test
(`clients/desktop/tests/test_ui_updates.py`) uses them with a local update host.

## Signing a manifest by hand

When the release key is kept offline instead of in the `release`
environment, leave `RELEASE_SIGNING_KEY` unset (the draft then has no
manifest) and, before publishing the draft:

1. Download the three installers from the draft.
2. On the machine with the key, from a checkout of the tag:

   ```sh
   V=2026.11.0; B=https://github.com/hyperkubeorg/openarcanum/releases/download/v$V
   uv run --project clients/desktop python clients/desktop/scripts/release_manifest.py sign \
     --key release.pem --version $V --expires-days 30 --out manifest.json \
     --asset linux-x86_64=openarcanum-$V-linux-x86_64.AppImage=$B/openarcanum-$V-linux-x86_64.AppImage \
     --asset windows-x86_64=openarcanum-$V-windows-x86_64-setup.exe=$B/openarcanum-$V-windows-x86_64-setup.exe \
     --asset macos-universal=openarcanum-$V-macos-arm64.dmg=$B/openarcanum-$V-macos-arm64.dmg
   uv run --project clients/desktop python clients/desktop/scripts/release_manifest.py verify --builtin manifest.json
   ```

3. Attach `manifest.json` to the draft (`gh release upload v$V manifest.json`),
   then publish. Re-sign and replace it (`--clobber`) before `expires_at`
   while this is the newest release.
