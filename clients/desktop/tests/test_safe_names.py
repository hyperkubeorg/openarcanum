"""File names from other people (names.safe_name, dmfiles.safe_name): one
safe path component on every OS, with nothing that makes it read as another
name, for DM attachments and community files alike."""

from __future__ import annotations

import unicodedata

import pytest
from hypothesis import given
from hypothesis import strategies as st

from openarcanum import community, dmfiles, names
from openarcanum.wire import community_pb2 as cp

RLO, LRI, PDI, LRM, RLM, ALM, ZWJ = "\u202e", "\u2066", "\u2069", "\u200e", "\u200f", "\u061c", "\u200d"


@pytest.mark.parametrize(
    ("hostile", "want"),
    [
        (f"invoice{RLO}fdp.exe", "invoicefdp.exe"),  # shown as "invoiceexe.pdf"
        (f"a{LRI}b{PDI}c{LRM}{RLM}{ALM}{ZWJ}.txt", "abc.txt"),
        ("\u202a\u202b\u202c\u202d\u202ex.png", "x.png"),
        ("CON", "_CON"),
        ("con.txt", "_con.txt"),
        ("CON .txt", "_CON .txt"),
        ("nul.tar.gz", "_nul.tar.gz"),
        ("COM1", "_COM1"),
        ("lpt9.log", "_lpt9.log"),
        ("AUX...", "_AUX"),
        ("report.pdf. . .", "report.pdf"),
        ("report.pdf   ", "report.pdf"),
        ("../../.bashrc", "bashrc"),
        ("/etc/passwd", "passwd"),
        ("C:\\Windows\\win.ini", "win.ini"),
        ("a\\..\\b.txt", "b.txt"),
        ("..", "file"),
        ("", "file"),
        (f"{RLO}{LRM}", "file"),
        ("a\nb\tc\x7f.txt", "a_b_c_.txt"),
        ('x<y>z:"|?*.txt', "x_y_z_____.txt"),
        ("line\u2028sep\u2029.txt", "linesep.txt"),
        ("CONSOLE.txt", "CONSOLE.txt"),  # not a device name
        ("photo of the garden.jpg", "photo of the garden.jpg"),
        ("Ünïcödé 日本.txt", "Ünïcödé 日本.txt"),
    ],
)
def test_hostile_names(hostile, want):
    assert names.safe_name(hostile) == want
    assert dmfiles.safe_name(hostile) == want


def _ok(out: str) -> None:
    assert out and len(out) <= names.MAX_LEN
    assert "/" not in out and "\\" not in out and ":" not in out
    assert not out.startswith((".", " ")) and not out.endswith((".", " "))
    for c in out:
        assert ord(c) >= 32
        assert unicodedata.category(c) not in ("Cf", "Cc", "Cs", "Co", "Cn", "Zl", "Zp"), repr(c)
    assert out.split(".")[0].rstrip(" .").upper() not in names._RESERVED
    assert names.safe_name(out) == out  # stable


@given(st.text(max_size=400))
def test_any_name_comes_out_safe(s):
    _ok(names.safe_name(s))


def test_community_file_names_are_made_safe():
    """Community attachments: the page shows, and suggests when saving, the
    safe name, not the sender's raw one with its path and bidi controls."""
    ref = cp.FileRef(file_id=7, name=f"../../evil{RLO}gpj.exe", size=3, mime="image/jpeg", key=b"k" * 32)
    assert community.file_dict(ref)["name"] == "evilgpj.exe"
