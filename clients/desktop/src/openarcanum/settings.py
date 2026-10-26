"""User settings: schema, defaults and validation (inventory: docs/settings.md).

Two stores:
- Preferences (config dir, plain TOML): appearance, window geometry, the data
  folder, automatic update checks and the maintainer nodes, which are needed
  before unlock. Nothing that identifies a person or a community goes here
  (the maintainer list says which nodes this device talks to;
  docs/threat-model.md "Client device").
- Vault settings (inside the encrypted vault): everything else.

Every setting has a default and a validator; `validate()` returns the cleaned
value or raises SettingError with a message fit for the UI.
"""

from __future__ import annotations

import base64
import binascii
import os
import re
import sys
import tomllib
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.paths import atomic_write

# Maintainer nodes used when this device names none: the project's own.
# `--maintainer URL` (for a self-hosted network or a
# local test network) takes their place for one run.
DEFAULT_MAINTAINERS: tuple[str, ...] = ("https://maintainer-01.openarcanum.org",)

# The test suite replaces the built-in list (comma-separated URLs) so no test
# reaches the public network. Ignored in a release (frozen) build.
TEST_BUILTIN_ENV = "OPENARCANUM_TEST_BUILTIN_MAINTAINERS"


def _builtin_maintainers(env: dict | None = None) -> tuple[str, ...]:
    env = os.environ if env is None else env
    test = env.get(TEST_BUILTIN_ENV, "")
    if test and not getattr(sys, "frozen", False):
        return tuple(u.strip() for u in test.split(",") if u.strip())
    return DEFAULT_MAINTAINERS


BUILTIN_MAINTAINERS: tuple[str, ...] = _builtin_maintainers()
_launch_maintainers: list[str] = []


MAX_MAINTAINERS = 8


def set_launch_maintainers(urls: list[str]) -> None:
    """From the command line (`--maintainer`): used instead of the built-in
    list when this device's preferences don't name their own maintainers."""
    _launch_maintainers[:] = _maintainer_urls(urls)


class SettingError(UserError, ValueError):
    pass


@dataclass(frozen=True)
class Setting:
    key: str
    default: Any
    validate: Callable[[Any], Any]
    store: str  # "prefs" or "vault"
    live: bool = True  # takes effect without a restart


def _choice(*options: str) -> Callable[[Any], str]:
    def v(x: Any) -> str:
        if x not in options:
            raise SettingError(_("must be one of: {options}").format(options=", ".join(options)))
        return x

    return v


def _bool(x: Any) -> bool:
    if not isinstance(x, bool):
        raise SettingError(_("must be true or false"))
    return x


def _int_range(lo: int, hi: int, allow_zero: bool = False) -> Callable[[Any], int]:
    def v(x: Any) -> int:
        if isinstance(x, bool) or not isinstance(x, int | float) or int(x) != x:
            raise SettingError(_("must be a whole number"))
        x = int(x)
        if allow_zero and x == 0:
            return 0
        if not lo <= x <= hi:
            raise SettingError(_("must be between {lo} and {hi}").format(lo=lo, hi=hi))
        return x

    return v


def _url_list(
    schemes: tuple[str, ...], max_items: int, allow_empty: bool, loopback_http: bool = False
) -> Callable[[Any], list[str]]:
    """URLs with one of `schemes`. With loopback_http, plain http:// is also
    accepted for this computer only (a local test network)."""

    def v(x: Any) -> list[str]:
        if isinstance(x, str):
            x = [p.strip() for p in re.split(r"[\s,]+", x) if p.strip()]
        if not isinstance(x, list) or not all(isinstance(i, str) for i in x):
            raise SettingError(_("must be a list of URLs"))
        out: list[str] = []
        for u in x:
            u = u.strip().rstrip("/") if schemes[0].startswith("http") else u.strip()
            p = urlparse(u)
            loopback = (p.hostname or "") in ("127.0.0.1", "localhost", "::1")
            local_http = loopback_http and p.scheme == "http" and loopback
            if (p.scheme not in schemes and not local_http) or not p.netloc:
                starts = _(" or ").join(s + "://" for s in schemes)
                if loopback_http:
                    raise SettingError(
                        _("{url} must start with {schemes} (http:// only for this computer)").format(
                            url=repr(u), schemes=starts
                        )
                    )
                raise SettingError(_("{url} must start with {schemes}").format(url=repr(u), schemes=starts))
            if u not in out:
                out.append(u)
        if not out and not allow_empty:
            raise SettingError(_("add at least one"))
        if len(out) > max_items:
            raise SettingError(_("at most {max_items}").format(max_items=max_items))
        return out

    return v


_maintainer_url_list = _url_list(("https",), MAX_MAINTAINERS, allow_empty=True, loopback_http=True)


def _maintainer_urls(x: Any) -> list[str]:
    """Maintainer entries: a node's URL, or `URL#OPERATORKEY` for a node of
    a maintainer cluster whose member list this device verifies with that
    key (nodes.py; docs/protocol.md §7.5). One entry per URL."""
    if isinstance(x, str):
        x = [p.strip() for p in re.split(r"[\s,]+", x) if p.strip()]
    if not isinstance(x, list) or not all(isinstance(i, str) for i in x):
        raise SettingError(_("must be a list of URLs"))
    out: list[str] = []
    seen: set[str] = set()
    for e in x:
        url, sep, key = e.strip().partition("#")
        (url,) = _maintainer_url_list([url])
        if sep:
            try:
                raw = base64.urlsafe_b64decode(key + "=" * (-len(key) % 4))
            except binascii.Error, ValueError:
                raw = b""
            if len(raw) != 32:
                raise SettingError(_("{entry} has a network fingerprint that isn't valid").format(entry=repr(e)))
        if not url or url in seen:
            continue
        seen.add(url)
        out.append(url + sep + key.strip().rstrip("="))
    if len(out) > MAX_MAINTAINERS:
        raise SettingError(_("at most {max_items}").format(max_items=MAX_MAINTAINERS))
    return out


def _str_list(max_items: int) -> Callable[[Any], list[str]]:
    def v(x: Any) -> list[str]:
        if not isinstance(x, list) or not all(isinstance(i, str) and 0 < len(i) <= 200 for i in x):
            raise SettingError(_("must be a list of ids"))
        if len(x) > max_items:
            raise SettingError(_("at most {max_items}").format(max_items=max_items))
        return list(dict.fromkeys(x))

    return v


def _device_name(x: Any) -> str:
    if not isinstance(x, str):
        raise SettingError(_("must be text"))
    x = x.strip()
    if not 1 <= len(x) <= 64 or any(ord(c) < 32 for c in x):
        raise SettingError(_("must be 1 to 64 characters"))
    return x


def _data_dir(x: Any) -> str:
    if x in ("", None):
        return ""
    if not isinstance(x, str):
        raise SettingError(_("must be a folder path"))
    p = Path(x).expanduser()
    if not p.is_absolute():
        raise SettingError(_("must be an absolute path"))
    if p.exists() and not p.is_dir():
        raise SettingError(_("is a file, not a folder"))
    parent = p if p.exists() else p.parent
    if not parent.exists() or not os.access(parent, os.W_OK):
        raise SettingError(_("folder can't be created or written"))
    return str(p)


def _device_choice(x: Any) -> str:
    if not isinstance(x, str) or len(x) > 200:
        raise SettingError(_("must be a device name"))
    return x or "default"


def _volumes(x: Any) -> dict[str, float]:
    """Per-user volume: member key -> 0..2 (0-200%)."""
    if not isinstance(x, dict) or len(x) > 5000:
        raise SettingError(_("must be a map of member keys to volumes"))
    out = {}
    for k, v in x.items():
        if not isinstance(k, str) or not 0 < len(k) <= 100 or isinstance(v, bool) or not isinstance(v, int | float):
            raise SettingError(_("must be a map of member keys to volumes"))
        if not 0 <= v <= 2:
            raise SettingError(_("volumes must be between 0 and 2"))
        out[k] = float(v)
    return out


COMMUNITY_NOTIFY_LEVELS = ("all", "mentions", "nothing")


def _community_notify(x: Any) -> dict[str, str]:
    """Per-community notification level: community id -> all, mentions or
    nothing. A community not listed follows notify_mode."""
    if not isinstance(x, dict) or len(x) > 500:
        raise SettingError(_("must be a map of communities to notification levels"))
    out = {}
    for k, v in x.items():
        if not isinstance(k, str) or not 0 < len(k) <= 200 or v not in COMMUNITY_NOTIFY_LEVELS:
            raise SettingError(_("must be a map of communities to notification levels"))
        out[k] = v
    return out


STATUS_LEVELS = ("off", "state", "full")


def _status_communities(x: Any) -> dict[str, str]:
    """Who sees my status in each community: off, state (online, idle, do
    not disturb) or full (also the custom text). A community not listed gets
    the state only (statuses.py)."""
    if not isinstance(x, dict) or len(x) > 500:
        raise SettingError(_("must be a map of communities to off, state or full"))
    out = {}
    for k, v in x.items():
        if not isinstance(k, str) or not 0 < len(k) <= 200 or v not in STATUS_LEVELS:
            raise SettingError(_("must be a map of communities to off, state or full"))
        out[k] = v
    return out


def _ptt_key(x: Any) -> str:
    if not isinstance(x, str) or not re.fullmatch(r"[A-Za-z0-9 ]{1,16}|F[0-9]{1,2}", x):
        raise SettingError(_("must be a single key name such as Space, V or F8"))
    return x


# Voice activity's threshold, dBFS, measured on the microphone after the
# engine's processing (call.js levelDb). Speech comes out of the engine's
# gain control at about -23 dBFS whatever its input level; steady room
# noise passes about 3 dB quieter than it went in, unchanged by the gain
# control (clients/desktop/tests/test_calls.py measures both). -40 sits 17 dB under
# speech and 8 dB above a loud room (-45 dBFS of fan or computer noise,
# which -50 lets through all the time).
VOICE_SENSITIVITY_DEFAULT = -40
VOICE_SENSITIVITY_OLD_DEFAULT = -50

# Vault settings that belong to this device, not synced to the account's
# other devices (devices.py).
PER_DEVICE = (
    "device_name",
    "voice_input",
    "voice_output",
    "voice_output_volume",
    "voice_local_mutes",
    "sounds",
    "sound_volume",
    "sounds_off",
)

# The sounds that can be turned off one by one (sounds.js SOUND_SWITCHES).
SOUND_SWITCHES = ("message", "dm", "mention", "join", "leave", "mute", "deafen", "call")


def _sound_list(x: Any) -> list[str]:
    if not isinstance(x, list) or not all(isinstance(i, str) and i in SOUND_SWITCHES for i in x):
        raise SettingError(_("must be a list of sounds: {options}").format(options=", ".join(SOUND_SWITCHES)))
    return list(dict.fromkeys(x))


SETTINGS: tuple[Setting, ...] = (
    # Appearance: preferences, readable before unlock.
    # Dark by default: the desktop's colour scheme often doesn't reach the app
    # (no desktop portal or session bus, as in a container), and "system"
    # then shows light.
    Setting("theme", "dark", _choice("system", "light", "dark"), "prefs"),
    Setting("text_size", "medium", _choice("small", "medium", "large", "larger"), "prefs"),
    Setting("compact", False, _bool, "prefs"),
    # Data: where data and cache live; preferences so it's known before unlock.
    Setting("data_dir", "", _data_dir, "prefs"),
    # Stay signed in on this device (keychain.py): the profile opens without
    # the passphrase when OpenArcanum starts. Settings → Security shows it the
    # other way round, as "Ask for my passphrase when OpenArcanum starts". Per
    # device, read before unlock. False for a preferences file without it
    # (a profile from before staying signed in was the default keeps asking);
    # creating, restoring or linking a profile turns it on. Changed only
    # through Core.set_remember (it needs the passphrase) or the one-time
    # offer (Core.answer_stay_signed_in).
    Setting("remember_device", False, _bool, "prefs"),
    # Updates: checked at startup, before unlock (docs/release.md). A check
    # tells the update host this computer's IP address.
    Setting("update_check", True, _bool, "prefs"),
    # Closing the window leaves the app running in the system tray
    # (openarcanum/tray.py). Per device: whether there is a tray is a property of
    # this desktop. Only takes effect where a tray exists.
    Setting("close_to_tray", True, _bool, "prefs"),
    # Security
    Setting("auto_lock_minutes", 0, _int_range(1, 1440, allow_zero=True), "vault"),
    # Identity: the default choice when joining.
    Setting("default_identity", "separate", _choice("separate", "reuse"), "vault"),
    # Notifications
    Setting("notifications", True, _bool, "vault"),
    Setting("notify_mode", "all", _choice("all", "mentions"), "vault"),
    Setting("muted_communities", [], _str_list(500), "vault"),
    Setting("muted_channels", [], _str_list(5000), "vault"),
    # Per community: all messages, mentions only or nothing (the rail's
    # right-click menu; Settings → Notifications). Changed through
    # Core.set_community_notify.
    Setting("community_notify", {}, _community_notify, "vault"),
    # Sounds (ui/js/sounds.js; Settings → Notifications → Sounds): on or off,
    # their volume in percent, and the ones turned off one by one. Per
    # device, like the speaker they play through.
    Setting("sounds", True, _bool, "vault"),
    Setting("sound_volume", 50, _int_range(0, 100), "vault"),
    Setting("sounds_off", [], _sound_list, "vault"),
    # Network. The maintainer nodes are per device and in preferences: they
    # are needed before unlock (first run, unlock, restore, linking), and
    # they aren't secret. Changed through Core.set_maintainers.
    Setting("maintainers", [], _maintainer_urls, "prefs"),
    # For an entry that names its maintainer cluster (URL#KEY): use every
    # member of the cluster, not only that node (Settings → Network, "Use
    # this network's other nodes").
    Setting("maintainer_cluster_nodes", True, _bool, "prefs"),
    Setting("preferred_relays", [], _url_list(("ws", "wss"), 8, allow_empty=True), "vault"),
    # Directory search on this device (directory.py): download the directory
    # and search locally, so maintainers never see the queries.
    Setting("directory_local", False, _bool, "vault"),
    # Data
    Setting("cache_limit_mb", 1024, _int_range(64, 102400), "vault"),
    # Voice
    Setting("voice_input", "default", _device_choice, "vault"),
    Setting("voice_output", "default", _device_choice, "vault"),
    # Voice activity, push to talk, or studio (always on, no processing, for
    # people who bring their own gate and processing).
    Setting("voice_mode", "vad", _choice("vad", "ptt", "studio"), "vault"),
    Setting("ptt_key", "Space", _ptt_key, "vault"),
    Setting("ptt_global", True, _bool, "vault"),
    Setting("voice_sensitivity", VOICE_SENSITIVITY_DEFAULT, _int_range(-100, 0), "vault"),
    # This device's call output level, percent (the speaker's own; not synced).
    Setting("voice_output_volume", 100, _int_range(0, 200), "vault"),
    Setting("video_input", "default", _device_choice, "vault"),
    Setting("voice_volumes", {}, _volumes, "vault"),
    # Members muted on this device only ("Mute for me" in a call): never
    # played, and not received (the relay is told).
    Setting("voice_local_mutes", [], _str_list(5000), "vault"),
    # A screen share sends the shared screen's or window's sound too.
    Setting("screen_share_audio", True, _bool, "vault"),
    # Privacy
    Setting("dm_policy", "contacts", _choice("contacts", "members"), "vault"),
    # Statuses (statuses.py; docs/user-guide.md "Your status"). Who sees it,
    # end-to-end encrypted: contacts see the state and custom text, each
    # community the state only, unless changed. Settings → Privacy & DMs and
    # the status picker's "Who can see this".
    Setting("status_contacts", "full", _choice(*STATUS_LEVELS), "vault"),
    Setting("status_contacts_scope", "all", _choice("all", "chosen"), "vault"),
    Setting("status_contacts_chosen", [], _str_list(5000), "vault"),
    Setting("status_communities", {}, _status_communities, "vault"),
    # Idle after this many minutes without input on this device (0 = never).
    Setting("status_idle_minutes", 10, _int_range(1, 240, allow_zero=True), "vault"),
    # Devices
    Setting("device_name", "", _device_name, "vault"),
)

BY_KEY = {s.key: s for s in SETTINGS}

# Changing any of these changes who sees the status: it's posted again.
STATUS_AUDIENCE_KEYS = ("status_contacts", "status_contacts_scope", "status_contacts_chosen", "status_communities")


def validate(key: str, value: Any) -> Any:
    s = BY_KEY.get(key)
    if s is None:
        raise SettingError(_("unknown setting {key}").format(key=repr(key)))
    try:
        return s.validate(value)
    except SettingError as e:
        raise SettingError(_("{setting} {problem}").format(setting=key.replace("_", " "), problem=e)) from None


def defaults(store: str) -> dict[str, Any]:
    out: dict[str, Any] = {}
    for s in SETTINGS:
        if s.store == store:
            out[s.key] = list(s.default) if isinstance(s.default, list) else s.default
    return out


def effective_maintainers(configured: list[str]) -> list[str]:
    """The nodes in use: this device's own list, else `--maintainer`, else
    the built-in list."""
    if configured:
        return list(configured)
    if _launch_maintainers:
        return list(_launch_maintainers)
    return list(BUILTIN_MAINTAINERS)


def maintainers_source(configured: list[str]) -> str:
    """Where effective_maintainers(configured) comes from: "own", "launch"
    (the --maintainer option) or "builtin"."""
    if configured:
        return "own"
    return "launch" if _launch_maintainers else "builtin"


def launch_maintainers() -> list[str]:
    return list(_launch_maintainers)


def check_new_maintainer(url: Any, current: list[str]) -> list[str]:
    """The maintainer editor's Add: `current` with `url` added, cleaned by the
    setting's own validator, or SettingError with a message for the dialog."""
    if not isinstance(url, str) or not url.strip():
        raise SettingError(_("Enter the node's address, such as https://node.example."))
    if any(c.isspace() or c == "," for c in url.strip()):
        raise SettingError(_("Add one address at a time, without spaces."))
    try:
        (clean,) = _maintainer_urls([url])
    except SettingError:
        if "#" in url:
            raise
        raise SettingError(_("Use https:// (http:// only for this computer).")) from None
    have = _maintainer_urls(current)
    if clean.partition("#")[0] in [h.partition("#")[0] for h in have]:
        raise SettingError(_("That node is already in the list."))
    if len(have) >= MAX_MAINTAINERS:
        raise SettingError(_("You can list at most {n} maintainer nodes.").format(n=MAX_MAINTAINERS))
    return [*have, clean]


# ---------------------------------------------------------------- preferences.toml


def _toml_value(v: Any) -> str:
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    if isinstance(v, str):
        return '"' + v.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n") + '"'
    if isinstance(v, list):
        return "[" + ", ".join(_toml_value(i) for i in v) + "]"
    raise TypeError(f"can't write {type(v).__name__} to TOML")


class Preferences:
    """Unencrypted preferences in the config directory.

    The file records its format version (`format`); a file without one is
    format 1. Every key is read and validated on its own and unknown keys are
    ignored, so a file from a newer format still yields the keys this build
    knows (the data folder above all)."""

    WINDOW_KEYS = ("window_x", "window_y", "window_width", "window_height")
    # One-time notices this device has shown (true once shown; not settings).
    # stay_signed_in_offered: the offer to stay signed in, made once to a
    # profile from before staying signed in was the default.
    SHOWN_KEYS = ("tray_hint_shown", "stay_signed_in_offered")
    # Where this device keeps the key that opens the profile at start
    # (keychain.py): "keychain", "file", or "" for nowhere (staying signed
    # in is off, or the profile was locked). Not a setting: it records state.
    UNLOCK_KEY_IN = "unlock_key_in"
    UNLOCK_KEY_PLACES = ("", "keychain", "file")
    # The maintainer sets of the clusters this device's entries pin
    # (nodes.py), as last verified: base64url SignedMaintainerSet bytes,
    # checked again on load. Not a setting: a cache, public data.
    MAINTAINER_SETS = "maintainer_sets"
    FORMAT = 1

    def __init__(self, path: Path):
        self.path = path
        self.values: dict[str, Any] = defaults("prefs")
        self.window: dict[str, int] = {}
        self.shown: set[str] = set()
        self.unlock_key_in = ""
        self.maintainer_sets: list[str] = []
        self.load()

    def load(self) -> None:
        try:
            data = tomllib.loads(self.path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return
        except tomllib.TOMLDecodeError, OSError, UnicodeDecodeError:
            # A damaged preferences file falls back to defaults; it holds
            # nothing that can't be chosen again.
            return
        for key, value in data.items():
            if key in self.WINDOW_KEYS and isinstance(value, int):
                self.window[key] = value
            elif key in self.SHOWN_KEYS and value is True:
                self.shown.add(key)
            elif key == self.UNLOCK_KEY_IN and value in self.UNLOCK_KEY_PLACES:
                self.unlock_key_in = value
            elif key == self.MAINTAINER_SETS and isinstance(value, list):
                self.maintainer_sets = [v for v in value if isinstance(v, str) and len(v) < 16384][:MAX_MAINTAINERS]
            elif key in BY_KEY and BY_KEY[key].store == "prefs":
                try:
                    self.values[key] = validate(key, value)
                except SettingError:
                    pass
        if self.UNLOCK_KEY_IN not in data and self.values["remember_device"]:
            # A file without this record predates keeping the key anywhere
            # but the OS keychain, so remember_device on means the keychain.
            self.unlock_key_in = "keychain"

    def save(self) -> None:
        lines = ["# OpenArcanum preferences. Nothing here identifies you.", f"format = {self.FORMAT}"]
        for k, v in self.values.items():
            lines.append(f"{k} = {_toml_value(v)}")
        for k, v in self.window.items():
            lines.append(f"{k} = {int(v)}")
        for k in sorted(self.shown):
            lines.append(f"{k} = true")
        lines.append(f"{self.UNLOCK_KEY_IN} = {_toml_value(self.unlock_key_in)}")
        if self.maintainer_sets:
            lines.append(f"{self.MAINTAINER_SETS} = {_toml_value(self.maintainer_sets)}")
        atomic_write(self.path, ("\n".join(lines) + "\n").encode())

    def set(self, key: str, value: Any) -> Any:
        clean = validate(key, value)
        if BY_KEY[key].store != "prefs":
            raise SettingError(_("{key} is not a preference").format(key=key))
        self.values[key] = clean
        self.save()
        return clean

    def mark_shown(self, key: str) -> None:
        if key not in self.SHOWN_KEYS:
            raise ValueError(key)
        if key not in self.shown:
            self.shown.add(key)
            self.save()

    def set_unlock_key_in(self, where: str) -> None:
        if where not in self.UNLOCK_KEY_PLACES:
            raise ValueError(where)
        if where != self.unlock_key_in:
            self.unlock_key_in = where
            self.save()

    def set_maintainer_sets(self, sets: list[str]) -> None:
        if sets != self.maintainer_sets:
            self.maintainer_sets = list(sets)
            self.save()

    def set_window(self, x: int, y: int, w: int, h: int) -> None:
        w, h = max(480, min(int(w), 10000)), max(360, min(int(h), 10000))
        self.window = {"window_x": int(x), "window_y": int(y), "window_width": w, "window_height": h}
        self.save()


# ---------------------------------------------------------------- passphrases

MIN_PASSPHRASE = 10


def passphrase_strength(p: str) -> dict:
    """A rough strength estimate for the meter: score 0 to 4."""
    if not p:
        return {
            "score": 0,
            "label": _("Empty"),
            "ok": False,
            "message": _("Use at least {min} characters.").format(min=MIN_PASSPHRASE),
        }
    classes = sum(bool(re.search(rx, p)) for rx in (r"[a-z]", r"[A-Z]", r"[0-9]", r"[^A-Za-z0-9 ]", r" "))
    unique = len(set(p))
    words = len(re.findall(r"[A-Za-z]{3,}", p))
    bits = len(p) * (1.5 + classes * 0.6) * min(1.0, unique / max(1, len(p) * 0.5))
    if words >= 4:
        bits = max(bits, words * 11.0)
    if re.fullmatch(r"(.)\1*", p) or p.lower() in {"password1234", "passwordpassword", "1234567890"}:
        bits = 5
    score = 0 if bits < 25 else 1 if bits < 40 else 2 if bits < 55 else 3 if bits < 75 else 4
    labels = [_("Very weak"), _("Weak"), _("Fair"), _("Strong"), _("Very strong")]
    ok = len(p) >= MIN_PASSPHRASE and score >= 2
    msg = ""
    if len(p) < MIN_PASSPHRASE:
        msg = _("Use at least {min} characters.").format(min=MIN_PASSPHRASE)
    elif score < 2:
        msg = _("Add more words or mix in other characters.")
    return {"score": score, "label": labels[score], "ok": ok, "message": msg}


def check_new_passphrase(p: str) -> None:
    st = passphrase_strength(p)
    if not st["ok"]:
        raise SettingError(st["message"] or _("Choose a stronger passphrase."))
