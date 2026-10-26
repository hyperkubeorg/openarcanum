"""A keyring backend for tests (this container has no Secret Service): a
JSON file named by OPENARCANUM_TEST_KEYRING_FILE. Selected with keyring's own
PYTHON_KEYRING_BACKEND=keyring_backend.FileKeyring (tests dir on the path).
CI runs the same tests against the real OS keychains.
OPENARCANUM_TEST_KEYRING_DELAY (seconds) makes reads slow, like a keychain
that asks the user first."""

import json
import os
import time
from pathlib import Path

from jaraco.classes import properties
from keyring.backend import KeyringBackend
from keyring.errors import PasswordDeleteError


class FileKeyring(KeyringBackend):
    @properties.classproperty
    def priority(cls) -> float:
        # Only usable when a test configured it (importing this module
        # registers the class with keyring's automatic choice).
        if "OPENARCANUM_TEST_KEYRING_FILE" not in os.environ:
            raise RuntimeError("the test keyring isn't configured")
        return 1

    @property
    def _path(self) -> Path:
        return Path(os.environ["OPENARCANUM_TEST_KEYRING_FILE"])

    def _load(self) -> dict:
        try:
            return json.loads(self._path.read_text())
        except FileNotFoundError:
            return {}

    def _save(self, d: dict) -> None:
        self._path.write_text(json.dumps(d))

    def get_password(self, service, username):
        time.sleep(float(os.environ.get("OPENARCANUM_TEST_KEYRING_DELAY", "0")))
        return self._load().get(f"{service}\0{username}")

    def set_password(self, service, username, password):
        d = self._load()
        d[f"{service}\0{username}"] = password
        self._save(d)

    def delete_password(self, service, username):
        d = self._load()
        if d.pop(f"{service}\0{username}", None) is None:
            raise PasswordDeleteError("not found")
        self._save(d)
