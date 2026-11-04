"""resolve_data_dir follows docs/security.md and must agree with the gateway's copy."""

from __future__ import annotations

import pytest

import dc

CASES = [
    # (platform, env, home, configured, expected)
    ("win32", {"LOCALAPPDATA": r"C:\Users\A\AppData\Local"}, r"C:\Users\A", None,
     r"C:\Users\A\AppData\Local\DigitalCommons"),
    ("win32", {}, r"C:\Users\A", None, r"C:\Users\A\AppData\Local\DigitalCommons"),
    ("win32", {"DC_DATA_DIR": r"D:\dc"}, r"C:\Users\A", None, r"D:\dc"),
    ("win32", {}, r"C:\Users\A", r"E:\conf", r"E:\conf"),
    ("win32", {"DC_DATA_DIR": r"D:\dc"}, r"C:\Users\A", r"E:\conf", r"D:\dc"),
    ("darwin", {}, "/Users/a", None, "/Users/a/Library/Application Support/DigitalCommons"),
    ("darwin", {"XDG_STATE_HOME": "/s"}, "/Users/a", None,
     "/Users/a/Library/Application Support/DigitalCommons"),
    ("linux", {"XDG_STATE_HOME": "/s"}, "/home/a", None, "/s/digital-commons"),
    ("linux", {"XDG_STATE_HOME": "relative"}, "/home/a", None, "/home/a/.local/state/digital-commons"),
    ("linux", {}, "/home/a", None, "/home/a/.local/state/digital-commons"),
    ("linux", {"DC_DATA_DIR": "/d"}, "/home/a", "/c", "/d"),
    ("linux", {}, "/home/a", "/c", "/c"),
]


@pytest.mark.parametrize("platform,env,home,configured,expected", CASES)
def test_resolution(platform, env, home, configured, expected):
    assert str(dc.resolve_data_dir(env, platform, home, configured)) == expected


@pytest.mark.parametrize(
    "platform,value",
    [("linux", "relative/dir"), ("linux", "./x"), ("win32", r"data\dc"), ("win32", "C:dc")],
)
def test_relative_path_is_refused(platform, value):
    with pytest.raises(dc.ToolError):
        dc.resolve_data_dir({"DC_DATA_DIR": value}, platform, "/home/a")


@pytest.mark.parametrize("platform,env,home,configured,expected", CASES)
def test_agrees_with_the_gateway(platform, env, home, configured, expected):
    paths = pytest.importorskip("app.security.paths")
    assert str(paths.resolve_data_dir(env, platform, home, configured)) == expected


def test_data_folder_uses_the_environment(tmp_path):
    settings = dc.load_settings({})
    assert dc.data_folder(settings) == tmp_path / "data"
