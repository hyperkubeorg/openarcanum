"""Where the client keeps its files.

This is the only module that works out storage paths. Everything else asks a
`Paths` instance.
"""

from __future__ import annotations

import os
import shutil
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

import platformdirs

from openarcanum import PRODUCT_NAME

POSIX = os.name == "posix"

# The browser engine's sandbox probe record (app.py decide_sandbox), in the
# data folder before any data-folder move applies.
ENGINE_SANDBOX = "engine-sandbox"

# The page the webview shows (index.html, styles.css, js/, strings/, brand/,
# vendor/): the web UI in clients/shared/ui, which the web client uses too.
# The app bundle carries it as openarcanum/ui beside this module
# (packaging/openarcanum.spec); from source it is read where it lies in the
# repository (clients/desktop/src/openarcanum -> clients/shared/ui).
_PACKAGE_DIR = Path(__file__).resolve().parent
UI_DIR = _PACKAGE_DIR / "ui" if getattr(sys, "frozen", False) else _PACKAGE_DIR.parents[2] / "shared" / "ui"


@dataclass(frozen=True)
class Paths:
    config: Path
    data: Path
    cache: Path
    log: Path

    @classmethod
    def default(cls) -> Paths:
        return cls(
            config=Path(platformdirs.user_config_dir(PRODUCT_NAME, appauthor=False)),
            data=Path(platformdirs.user_data_dir(PRODUCT_NAME, appauthor=False)),
            cache=Path(platformdirs.user_cache_dir(PRODUCT_NAME, appauthor=False)),
            log=Path(platformdirs.user_log_dir(PRODUCT_NAME, appauthor=False)),
        )

    @classmethod
    def under(cls, home: Path) -> Paths:
        """All four directories inside one folder (`--home DIR`)."""
        home = Path(home).expanduser().resolve()
        return cls(config=home / "config", data=home / "data", cache=home / "cache", log=home / "log")

    def with_data_location(self, folder: Path | None) -> Paths:
        """Apply the "data folder location" setting: data and cache move under it."""
        if folder is None:
            return self
        folder = Path(folder).expanduser().resolve()
        return Paths(config=self.config, data=folder / "data", cache=folder / "cache", log=self.log)

    def ensure(self) -> None:
        for d in (self.config, self.data, self.cache, self.log):
            ensure_dir(d, private=True)

    @property
    def preferences(self) -> Path:
        return self.config / "preferences.toml"

    @property
    def vault(self) -> Path:
        return self.data / "vault.bin"

    @property
    def state(self) -> Path:
        return self.data / "state.bin"

    @property
    def unlock_guard(self) -> Path:
        return self.data / "unlock-guard.json"

    def community_cache(self, community_hex: str) -> Path:
        return self.cache / "communities" / community_hex

    @property
    def log_file(self) -> Path:
        return self.log / "client.log"


def read_at_most(path: Path, limit: int) -> bytes | None:
    """The file's bytes, or None when it is larger than `limit` (checked
    before anything is read: a multi-GB file picked by mistake stays on
    disk)."""
    if path.stat().st_size > limit:
        return None
    data = path.read_bytes()
    return None if len(data) > limit else data


def ensure_dir(path: Path, private: bool = False) -> None:
    """Create path for the app, readable by its user only. A directory that
    already exists keeps its mode (it may be one the user named, such as
    `--home .`), unless it's `private`: one of the app's own folders."""
    new = not path.is_dir()
    path.mkdir(parents=True, exist_ok=True)
    if POSIX and (new or private):
        os.chmod(path, 0o700)


def atomic_write(path: Path, data: bytes, mode: int = 0o600) -> None:
    """Write to a temp file in the same directory, fsync, then rename over path."""
    ensure_dir(path.parent)
    fd, tmp = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        if POSIX:
            os.fchmod(fd, mode)
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
        if POSIX:
            dfd = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(dfd)
            finally:
                os.close(dfd)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def move_tree(src: Path, dst: Path) -> None:
    """Move a directory's contents into dst (used when the data folder
    moves). Nothing at dst is overwritten (FileExistsError), and the profile
    file goes last: a move that fails midway leaves the profile where it was."""
    if not src.exists() or src.resolve() == dst.resolve():
        return
    ensure_dir(dst)
    for child in sorted(src.iterdir(), key=lambda c: c.name == "vault.bin"):
        target = dst / child.name
        if target.exists():
            raise FileExistsError(str(target))
        shutil.move(str(child), str(target))
    try:
        src.rmdir()
    except OSError:
        pass


def save_folder() -> Path:
    """Where Save… suggests putting a file: the Downloads folder, or the home
    folder on systems that have none."""
    downloads = Path(platformdirs.user_downloads_dir())
    return downloads if downloads.is_dir() else Path.home()


def platform_name() -> str:
    if sys.platform.startswith("linux"):
        return "Linux"
    if sys.platform == "darwin":
        return "macOS"
    if sys.platform.startswith("win"):
        return "Windows"
    return sys.platform
