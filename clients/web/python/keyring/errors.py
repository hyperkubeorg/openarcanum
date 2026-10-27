class KeyringError(Exception):
    pass


class PasswordDeleteError(KeyringError):
    pass


class PasswordSetError(KeyringError):
    pass


class NoKeyringError(KeyringError, RuntimeError):
    pass


class InitError(KeyringError):
    pass
