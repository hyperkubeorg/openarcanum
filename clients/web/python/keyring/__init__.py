"""Stands in for the keyring package in the browser, which has no OS keychain.

keychain.available() sees the fail backend and the client keeps the unlock
key in its key file instead, which in the browser lives in the site's storage
(the web client's weaker trust model, docs/threat-model.md).
"""

from keyring import errors
from keyring.backends import fail


def get_keyring():
    return fail.Keyring()


def get_password(service, username):
    raise errors.NoKeyringError("no keychain in the browser")


def set_password(service, username, password):
    raise errors.NoKeyringError("no keychain in the browser")


def delete_password(service, username):
    raise errors.NoKeyringError("no keychain in the browser")
