"""The installer build and the workflow that runs it. Building one needs the platform it
is for, so these check what can be checked anywhere, and GitHub builds the rest."""

from __future__ import annotations

import importlib.util
import re
import struct

import pytest
import yaml
from conftest import ROOT

import dc

INSTALLER = ROOT / "installer"
WORKFLOWS = ROOT / ".github" / "workflows"


def load_build():
    spec = importlib.util.spec_from_file_location("build", INSTALLER / "build.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


build = load_build()


@pytest.fixture(scope="module")
def workflow():
    return yaml.safe_load((WORKFLOWS / "installers.yml").read_text(encoding="utf-8"))


def test_every_python_build_is_pinned_to_a_checksum():
    assert set(build.PYTHON_BUILDS) == {"windows-x64", "macos-arm64", "macos-x64"}
    for name, digest in build.PYTHON_BUILDS.values():
        assert re.fullmatch(r"[0-9a-f]{64}", digest), name
        assert name.endswith("-install_only.tar.gz")
    release = build.PYTHON_RELEASE.rsplit("/", 1)[-1]
    assert all(f"+{release}-" in name for name, _ in build.PYTHON_BUILDS.values())


def test_the_version_comes_from_the_gateway():
    assert re.fullmatch(r"\d+\.\d+\.\d+", build.app_version())


def test_everything_an_installed_copy_needs_is_in_the_repository():
    for relative in build.APP_FILES:
        assert (ROOT / relative).exists(), relative


def test_the_icons_are_well_formed(tmp_path, monkeypatch):
    monkeypatch.setattr(build, "png", lambda size: b"\x89PNG\r\n\x1a\n" + bytes(size))
    build.write_png_icon(tmp_path / "a.ico", "ico")
    data = (tmp_path / "a.ico").read_bytes()
    reserved, kind, count = struct.unpack("<HHH", data[:6])
    assert (reserved, kind, count) == (0, 1, 4)
    build.write_png_icon(tmp_path / "a.icns", "icns")
    icns = (tmp_path / "a.icns").read_bytes()
    assert icns[:4] == b"icns" and struct.unpack(">I", icns[4:8])[0] == len(icns)


def test_a_real_icon_image_is_a_png():
    image = build.png(16)
    assert image.startswith(b"\x89PNG\r\n\x1a\n") and image.endswith(b"IEND\xaeB`\x82")


def test_windows_installs_for_this_user_and_starts_with_dc_start():
    script = (INSTALLER / "windows" / "DigitalCommons.iss").read_text(encoding="utf-8")
    assert "PrivilegesRequired=lowest" in script
    assert "AppId={{6F1B2E7C-5A41-4C8E-9C0B-0D3A9E5B7F21}" in script
    assert script.count('""{app}\\dc.py"" start') == 3
    assert (INSTALLER / "windows" / "before-install.txt").is_file()
    assert "MB_DEFBUTTON2, IDNO" in script  # Keeping the conversations is the default.


def test_the_mac_scripts_open_terminal_and_run_dc_start():
    launcher = (INSTALLER / "macos" / "launcher.sh").read_bytes()
    command = (INSTALLER / "macos" / "start.command").read_bytes()
    for script in (launcher, command):
        assert script.startswith(b"#!/bin/sh\n") and b"\r" not in script
    assert b"open -a Terminal" in launcher
    assert b"./runtime/bin/python3 dc.py start" in command
    assert command.rstrip().endswith(b"dc.py start")


def test_the_launcher_paths_match_dc_py():
    assert dc.runtime_python(ROOT / "runtime", windows=True) == ROOT / "runtime" / "python.exe"
    assert dc.runtime_python(ROOT / "runtime", windows=False) == ROOT / "runtime" / "bin" / "python3"


def test_every_workflow_pins_each_action_to_one_commit():
    commits: dict = {}
    for path in WORKFLOWS.glob("*.yml"):
        for action in re.findall(r"uses:\s*(\S+)", path.read_text(encoding="utf-8")):
            assert re.fullmatch(r"[\w./-]+@[0-9a-f]{40}", action), (path.name, action)
            name, commit = action.split("@")
            commits.setdefault(name, set()).add(commit)
    assert all(len(found) == 1 for found in commits.values()), commits


def test_only_the_release_job_may_write(workflow):
    assert workflow["permissions"] == {"contents": "read"}
    jobs = workflow["jobs"]
    assert "permissions" not in jobs["installer"]
    assert jobs["release"]["permissions"] == {"contents": "write"}
    assert jobs["release"]["if"] == "startsWith(github.ref, 'refs/tags/v')"
    command = jobs["release"]["steps"][-1]["run"]
    assert "--draft" in command and "--prerelease" in command


def test_each_platform_is_built_installed_and_run(workflow):
    job = workflow["jobs"]["installer"]
    targets = {row["target"]: row["runner"] for row in job["strategy"]["matrix"]["include"]}
    assert targets == {"windows-x64": "windows-latest", "macos-arm64": "macos-latest", "macos-x64": "macos-26-intel"}
    names = [step.get("name", "") for step in job["steps"]]
    order = [
        "Build the installer",
        "Run the installed copy in a real console, in demo mode",
        "First start on a machine without Ollama",
        "One real chat through the installed gateway",
        "Uninstall it on Windows",
    ]
    assert [names.index(name) for name in order] == sorted(names.index(name) for name in order)
    first_start = next(step for step in job["steps"] if step.get("name") == "First start on a machine without Ollama")
    assert "start --prepare-only --yes" in first_start["run"]
