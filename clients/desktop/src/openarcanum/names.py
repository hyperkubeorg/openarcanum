"""File names that come from other people (attachments in DMs and communities),
made safe to show and to save under on any OS."""

from __future__ import annotations

import unicodedata

# Windows device names, reserved with any extension and with trailing dots or
# spaces before it ("CON", "con.txt", "CON .txt", "nul.tar.gz", "COM1").
_RESERVED = {
    "CON",
    "PRN",
    "AUX",
    "NUL",
    "CONIN$",
    "CONOUT$",
    *(f"COM{i}" for i in [*range(1, 10), "¹", "²", "³"]),
    *(f"LPT{i}" for i in [*range(1, 10), "¹", "²", "³"]),
}
_FORBIDDEN = set('<>:"|?*\\/\x7f')
MAX_LEN = 200


def _allowed(c: str) -> bool:
    if ord(c) < 32 or c in _FORBIDDEN:
        return False
    # Format characters (Cf: bidi overrides and isolates U+202A to U+202E,
    # U+2066 to U+2069, marks U+200E/U+200F/U+061C, zero-width joiners, ...),
    # line and paragraph separators, C1 controls, surrogates and private use
    # can make a name read as something else ("gnp.exe" shown as "exe.png").
    return unicodedata.category(c) not in ("Cf", "Cc", "Cs", "Co", "Cn", "Zl", "Zp")


def safe_name(name: str) -> str:
    """A file name from someone else, made safe to save under on any OS: one
    path component (no folders, no drive), no control, format (bidi) or
    reserved characters, not hidden, no trailing dots or spaces (which Windows
    drops), not a Windows device name, at most 200 characters; "file" when
    nothing is left."""
    name = str(name).replace("\\", "/").split("/")[-1]
    name = "".join(c if _allowed(c) else ("_" if ord(c) < 32 or c in _FORBIDDEN else "") for c in name)
    name = " ".join(name.split())  # every kind of whitespace run is one space
    name = name.lstrip(". ").rstrip(". ")
    if name.split(".")[0].rstrip(" .").upper() in _RESERVED:
        name = "_" + name
    name = name[:MAX_LEN].rstrip(". ")
    return name or "file"
