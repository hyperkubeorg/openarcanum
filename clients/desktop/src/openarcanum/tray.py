"""The app icon and the system tray icon (docs/user-guide.md "The tray icon").

While the app runs, where the desktop has a system tray, OpenArcanum shows its
icon there: a click shows the window, and its menu has Open OpenArcanum, Mute
notifications, Lock and Quit OpenArcanum. The icon gets a red dot while
something waits for the user (unread mentions, unread direct messages,
reports and join requests waiting for review: the window's own badges,
summed), and its tooltip says what. Closing the window can leave the app
running there (the `close_to_tray` preference; app.py decides).

Most Linux desktops show tray icons through the StatusNotifierItem D-Bus
protocol (KDE, Xfce, Cinnamon, GNOME with the AppIndicator extension), some
through the older X11 tray; plain GNOME and many Wayland sessions have
neither, and QSystemTrayIcon.isSystemTrayAvailable() says so.

`tray_view` is the state -> icon and tooltip logic, without Qt. `Tray` is
the Qt side and only runs on the Qt main thread.
"""

from __future__ import annotations

import sys
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from openarcanum import PRODUCT_NAME
from openarcanum.i18n import _, ngettext
from openarcanum.paths import UI_DIR

BRAND = UI_DIR / "brand"
# The sizes clients/desktop/packaging/icons.py renders (icons.APP_SIZES, ALERT_SIZES).
APP_SIZES = (16, 24, 32, 48, 64, 128, 256)
ALERT_SIZES = (16, 22, 24, 32, 48, 64)
# Counts sent by the page are clamped to this (they're only shown).
MAX_COUNT = 1_000_000


def icon_files(alert: bool = False) -> list[Path]:
    """The PNGs of the app icon (alert: the tray icon with the red dot),
    smallest first."""
    if alert:
        return [BRAND / f"openarcanum-alert-{n}.png" for n in ALERT_SIZES]
    return [BRAND / f"openarcanum-{n}.png" for n in APP_SIZES]


@dataclass(frozen=True)
class Attention:
    """What waits for the user, as the window's badges count it."""

    mentions: int = 0  # unread mentions in communities
    direct: int = 0  # unread messages in direct and group conversations
    review: int = 0  # reports and join requests waiting for this moderator

    @classmethod
    def clean(cls, mentions, direct, review) -> Attention:
        def n(v) -> int:
            if isinstance(v, bool) or not isinstance(v, int | float) or v != v:
                return 0
            return max(0, min(int(v), MAX_COUNT))

        return cls(n(mentions), n(direct), n(review))

    @property
    def waiting(self) -> bool:
        return bool(self.mentions or self.direct or self.review)


def tray_view(attention: Attention, profile: str, muted: bool) -> tuple[bool, str]:
    """The tray icon for a state: (show the red dot, tooltip). `profile` is
    "unlocked", "locked" or "no_profile". Only an unlocked profile has
    anything to count: locked, the window shows nothing either."""
    lines = [PRODUCT_NAME]
    alert = False
    if profile == "locked":
        lines.append(_("Locked"))
    elif profile == "unlocked":
        parts = []
        if attention.mentions:
            n = attention.mentions
            parts.append(ngettext("{n} unread mention", "{n} unread mentions", n).format(n=n))
        if attention.direct:
            n = attention.direct
            parts.append(ngettext("{n} unread direct message", "{n} unread direct messages", n).format(n=n))
        if attention.review:
            n = attention.review
            parts.append(ngettext("{n} waiting for review", "{n} waiting for review", n).format(n=n))
        alert = bool(parts)
        lines += parts or [_("Nothing new")]
    if muted and profile == "unlocked":
        lines.append(_("Notifications muted"))
    return alert, "\n".join(lines)


def quit_keys() -> str:
    return "Cmd+Q" if sys.platform == "darwin" else "Ctrl+Q"


def hidden_hint() -> tuple[str, str]:
    """The one-time notice when closing the window first leaves the app
    running: (title, body)."""
    return (
        _("OpenArcanum is still running"),
        _(
            "It keeps running in the system tray so messages and calls still reach you. To quit, choose "
            "Quit OpenArcanum in the tray icon's menu or press {keys}. To quit whenever you close the window, "
            "turn off Settings → Appearance → Keep running in the tray."
        ).format(keys=quit_keys()),
    )


@dataclass
class TrayActions:
    """What the tray's clicks do, and what it shows (app.py `Gui`)."""

    show_window: Callable[[], None]
    set_muted: Callable[[bool], None]
    lock: Callable[[], None]
    quit: Callable[[], None]
    # "unlocked", "locked" or "no_profile": mute and lock need an open profile.
    profile: Callable[[], str]
    # Desktop notifications are off (Settings → Notifications).
    muted: Callable[[], bool]


def qicon(alert: bool = False):
    from qtpy.QtGui import QIcon

    icon = QIcon()
    for f in icon_files(alert):
        icon.addFile(str(f))
    return icon


class Tray:
    """The QSystemTrayIcon and its menu. Qt main thread only."""

    def __init__(self, actions: TrayActions):
        from qtpy.QtWidgets import QMenu, QSystemTrayIcon

        self.actions = actions
        self.attention = Attention()
        self.alert = False
        self._icons = {False: qicon(False), True: qicon(True)}
        self.icon = QSystemTrayIcon(self._icons[False])
        menu = QMenu()
        self.open_action = menu.addAction(_("Open OpenArcanum"))
        self.open_action.triggered.connect(lambda *_a: actions.show_window())
        font = self.open_action.font()
        font.setBold(True)
        self.open_action.setFont(font)
        self.mute_action = menu.addAction(_("Mute notifications"))
        self.mute_action.setCheckable(True)
        self.mute_action.triggered.connect(self._mute_clicked)
        self.lock_action = menu.addAction(_("Lock"))
        self.lock_action.triggered.connect(lambda *_a: actions.lock())
        menu.addSeparator()
        self.quit_action = menu.addAction(_("Quit OpenArcanum"))
        self.quit_action.triggered.connect(lambda *_a: actions.quit())
        menu.aboutToShow.connect(self.refresh)
        self.menu = menu  # the tray doesn't own it
        self.icon.setContextMenu(menu)
        self.icon.activated.connect(self._activated)
        self.refresh()

    def show(self) -> None:
        self.icon.show()

    def hide(self) -> None:
        self.icon.hide()

    def visible(self) -> bool:
        return self.icon.isVisible()

    def _activated(self, reason) -> None:
        from qtpy.QtWidgets import QSystemTrayIcon

        if reason in (QSystemTrayIcon.ActivationReason.Trigger, QSystemTrayIcon.ActivationReason.DoubleClick):
            self.actions.show_window()

    def _mute_clicked(self, checked: bool) -> None:
        self.actions.set_muted(bool(checked))

    def set_attention(self, attention: Attention) -> None:
        self.attention = attention
        self.refresh()

    def refresh(self) -> None:
        """Icon, tooltip and menu from the current state."""
        profile = self.actions.profile()
        unlocked = profile == "unlocked"
        muted = unlocked and self.actions.muted()
        alert, tip = tray_view(self.attention, profile, muted)
        if alert != self.alert:
            self.alert = alert
            self.icon.setIcon(self._icons[alert])
        if self.icon.toolTip() != tip:
            self.icon.setToolTip(tip)
        self.mute_action.setEnabled(unlocked)
        self.mute_action.setChecked(muted)
        self.lock_action.setEnabled(unlocked)

    def message(self, title: str, body: str) -> None:
        from qtpy.QtWidgets import QSystemTrayIcon

        self.icon.showMessage(title, body, QSystemTrayIcon.MessageIcon.Information, 6000)
