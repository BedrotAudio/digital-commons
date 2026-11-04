"""Finding Go, and the instructions printed when it is missing."""

from __future__ import annotations

import os
import stat
import sys

import pytest

import dc

GO_NAME = "go.exe" if sys.platform == "win32" else "go"


def fake_go(folder):
    folder.mkdir(parents=True, exist_ok=True)
    path = folder / GO_NAME
    path.write_bytes(b"")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return path


def test_path_comes_first(tmp_path):
    on_path = fake_go(tmp_path / "onpath")
    fake_go(tmp_path / "repo" / ".tools" / "go" / "bin")
    found = dc.find_go(str(on_path.parent), tmp_path / "repo", sys.platform == "win32")
    assert found is not None and found.resolve() == on_path.resolve()


def test_falls_back_to_the_copy_in_tools(tmp_path):
    local = fake_go(tmp_path / "repo" / ".tools" / "go" / "bin")
    found = dc.find_go(str(tmp_path / "empty"), tmp_path / "repo", sys.platform == "win32")
    assert found == local


def test_nothing_found_returns_none(tmp_path):
    assert dc.find_go(str(tmp_path / "empty"), tmp_path / "repo", sys.platform == "win32") is None


def test_an_empty_path_still_finds_the_local_copy(tmp_path):
    local = fake_go(tmp_path / "repo" / ".tools" / "go" / "bin")
    assert dc.find_go(os.pathsep, tmp_path / "repo", sys.platform == "win32") == local


def test_a_folder_named_go_is_not_a_toolchain(tmp_path):
    (tmp_path / "repo" / ".tools" / "go" / "bin" / GO_NAME).mkdir(parents=True)
    assert dc.find_go(str(tmp_path / "empty"), tmp_path / "repo", sys.platform == "win32") is None


@pytest.mark.parametrize(
    "platform,expected",
    [
        ("win32", "winget install GoLang.Go"),
        ("darwin", "brew install go"),
        ("linux", "sudo snap install go --classic"),
    ],
)
def test_install_instruction_per_platform(platform, expected):
    assert dc.go_install_instruction(platform) == expected


def test_missing_message_names_the_instruction(monkeypatch):
    monkeypatch.setattr(dc.sys, "platform", "win32")
    message = dc.go_missing_message()
    assert "winget install GoLang.Go" in message
    assert ".tools/go/bin" in message


def test_nothing_is_downloaded_when_go_is_missing():
    source = dc.Path(dc.__file__).read_text(encoding="utf-8")
    assert "urlretrieve" not in source and "zipfile" not in source


@pytest.mark.parametrize(
    "text,expected",
    [("go1.27.3", (1, 27, 3)), ("go version go1.26 windows/amd64", (1, 26)), ("", ())],
)
def test_parse_version(text, expected):
    assert dc.parse_version(text) == expected


def test_versions_compare_numerically():
    assert dc.parse_version("1.9.0") < dc.parse_version("1.10.0")


def test_go_mod_version(tmp_path):
    (tmp_path / "go.mod").write_text("module x\n\ngo 1.26.0\n\nrequire y v1\n", encoding="utf-8")
    assert dc.go_mod_version(tmp_path / "go.mod") == "1.26.0"
    assert dc.go_mod_version(tmp_path / "missing.mod") is None


def test_the_toolchain_is_never_fetched_automatically(monkeypatch):
    monkeypatch.delenv("GOTOOLCHAIN", raising=False)
    assert dc.go_env()["GOTOOLCHAIN"] == "local"
    monkeypatch.setenv("GOTOOLCHAIN", "auto")
    assert dc.go_env()["GOTOOLCHAIN"] == "auto"


def test_main_package_prefers_the_named_command(tmp_path):
    for name in ("alpha", "digital-commons"):
        (tmp_path / "cmd" / name).mkdir(parents=True)
        (tmp_path / "cmd" / name / "main.go").write_text("package main\n", encoding="utf-8")
    assert dc.find_main_package(tmp_path) == "./cmd/digital-commons"


def test_main_package_at_the_module_root(tmp_path):
    (tmp_path / "main.go").write_text("package main\n", encoding="utf-8")
    assert dc.find_main_package(tmp_path) == "."


def test_no_main_package_is_an_error(tmp_path):
    with pytest.raises(dc.ToolError):
        dc.find_main_package(tmp_path)


def test_the_real_terminal_package_is_found():
    assert dc.find_main_package(dc.TERMINAL) == "./cmd/digital-commons"


def test_rebuild_is_needed_only_when_a_source_is_newer(tmp_path):
    binary = tmp_path / "bin"
    source = tmp_path / "main.go"
    source.write_text("package main\n", encoding="utf-8")
    assert dc.needs_build(binary, [source])
    binary.write_bytes(b"x")
    os.utime(source, (1_000_000, 1_000_000))
    os.utime(binary, (2_000_000, 2_000_000))
    assert not dc.needs_build(binary, [source])
    os.utime(source, (3_000_000, 3_000_000))
    assert dc.needs_build(binary, [source])
