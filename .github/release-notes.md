OpenArcanum @VERSION@. Built by the Release workflow from tag `@TAG@` (`.github/workflows/release.yml`). Installers bundle everything they need: you don't need to install Python or Go.

## Desktop app

| System | File | Install |
|---|---|---|
| Windows 10/11 (x64) | `openarcanum-@VERSION@-windows-x86_64-setup.exe` | Run it. It installs for your user only (no administrator rights), adds a Start menu entry and an uninstaller (Settings → Apps). |
| macOS 14+ (Apple silicon) | `openarcanum-@VERSION@-macos-arm64.dmg` | Open it and drag the app to Applications. |
| Linux (x86-64, glibc 2.35+) | `openarcanum-@VERSION@-linux-x86_64.AppImage` | `chmod +x` it and run it (without FUSE: `--appimage-extract-and-run`). The `.tar.gz` holds the same app as a folder. |

**If the build is unsigned** (the release workflow logs an "Unsigned Windows installer" or "Unsigned macOS build" notice when the signing secrets aren't set):

- **Windows SmartScreen** says "Windows protected your PC": click **More info**, then **Run anyway**.
- **macOS Gatekeeper** says the app "can't be opened" or "is damaged": in Finder, Control-click the app in Applications, choose **Open**, then **Open** again; on macOS 15 and later, open it once, then go to System Settings → Privacy & Security and click **Open Anyway**. Or, in Terminal: `xattr -dr com.apple.quarantine /Applications/OpenArcanum.app`.
- **Linux** needs no signature.

Automatic updates for installed copies: @MANIFEST@.

## Servers

- Container images (linux/amd64 and linux/arm64), signed with cosign:
  `ghcr.io/hyperkubeorg/openarcanum-relay:@VERSION@`, `ghcr.io/hyperkubeorg/openarcanum-maintainer:@VERSION@`, `ghcr.io/hyperkubeorg/openarcanum-community:@VERSION@`.
- Static binaries: `openarcanum-servers-@VERSION@-linux-amd64.tar.gz`, `openarcanum-servers-@VERSION@-linux-arm64.tar.gz`.

## Verify the downloads

```sh
sha256sum --check --ignore-missing SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "@REPO_URL@/.github/workflows/release.yml@refs/tags/@TAG@" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

The source is at @REPO_URL@ (AGPL-3.0-only).

SHA-256 checksums:
