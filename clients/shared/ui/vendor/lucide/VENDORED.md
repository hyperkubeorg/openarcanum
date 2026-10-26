# Vendored: Lucide icons 1.61.0 (only the icons the app uses)

The interface's icons (buttons, the community rail, the voice bar and call
stage, channel kinds, flags). The page draws them as inline SVG built with
DOM calls from `ui/js/icons.js`, which `scripts/ui-icons` generates from the
files here; nothing is loaded at run time and no markup is parsed.

## Upstream

| | |
|---|---|
| Project | https://lucide.dev, https://github.com/lucide-icons/lucide |
| Package | npm `lucide-static` 1.61.0, https://registry.npmjs.org/lucide-static/-/lucide-static-1.61.0.tgz |
| Tarball sha256 | `62773cd2ee411ad2e37c1973d1e4c4b49f9a6c57da6b5d5d0abca08756ba75f1` |
| npm integrity | `sha512-yLAiQB3MWUJEstsEDAwenSA3m7vr1tdLvgnC16Q/XMxRRma0DWwMs8M2bWlvgShrIs1m2mWMJ+0AiahqQO4GrA==` (matches the registry's published value) |
| Licence | ISC (some icons derive from Feather, MIT); `LICENSE` here is the package's own file, unchanged |

## What is here

- `LICENSE`: from the package root.
- `icons/*.svg`: the package's `icons/<name>.svg` files for the icons the
  page uses, byte for byte.
- `SHA256SUMS`: the sha256 of each of those files.

No local changes. `scripts/ui-icons --check` (run by `make check` through
`clients/desktop/tests/test_ui_icons.py`) fails if a file here doesn't match
`SHA256SUMS`, if `ui/js/icons.js` is stale, if the page uses an icon that
isn't here, or if an icon here is unused.

## Adding or updating an icon

1. Download the release tarball above (or a newer one; then update this
   file's table) and check its sha256.
2. Copy `package/icons/<name>.svg` to `icons/` unchanged and add its line to
   `SHA256SUMS` (`sha256sum icons/<name>.svg`).
3. Run `scripts/ui-icons` and use the icon as `icon('<name>')` or h()'s
   `icon: '<name>'`.
