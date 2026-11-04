"""What dc.py says and does on macOS and Linux, with the platform injected.

These run on every computer, so a Windows run still checks the Mac paths. Tests that
need a real POSIX kernel are in test_gateway_lifecycle.py.
"""

from __future__ import annotations

import os
import re
import subprocess
from pathlib import PurePosixPath, PureWindowsPath

import pytest
from conftest import ROOT

import dc


@pytest.fixture(params=["darwin", "linux"])
def posix_platform(request, monkeypatch):
    monkeypatch.setattr(dc.sys, "platform", request.param)
    return request.param


def test_no_message_writes_the_interpreter_by_hand():
    body = (ROOT / "dc.py").read_text(encoding="utf-8").split('"""', 2)[2]
    assert not re.search(r"python3? dc\.py", body)


def test_the_helper_reads_the_platform_when_it_is_called(monkeypatch):
    monkeypatch.setattr(dc.sys, "platform", "darwin")
    assert dc.run_hint("dev") == "python3 dc.py dev"
    monkeypatch.setattr(dc.sys, "platform", "win32")
    assert dc.run_hint("dev") == "python dc.py dev"


def test_the_help_text_names_python3(posix_platform):
    assert "python3 dc.py setup" in dc.build_parser().format_help()


def test_a_missing_environment_names_python3(posix_platform, monkeypatch, tmp_path):
    monkeypatch.setattr(dc, "venv_python", lambda: tmp_path / "missing")
    with pytest.raises(dc.ToolError, match=r"Run: python3 dc\.py setup"):
        dc.require_venv()


def test_a_slow_gateway_names_python3(posix_platform, monkeypatch):
    monkeypatch.setattr(dc, "gateway_healthy", lambda url: False)
    with pytest.raises(dc.ToolError, match=r"python3 dc\.py doctor"):
        dc.wait_for_gateway("http://127.0.0.1:1", timeout=0)


def test_go_is_installed_with_brew_on_a_mac(monkeypatch):
    monkeypatch.setattr(dc.sys, "platform", "darwin")
    assert "brew install go" in dc.go_missing_message()


def test_python_is_installed_with_brew_on_a_mac():
    assert "brew install python@3.12" in dc.python_too_old_message((3, 9), "darwin")


def test_the_restart_note_fits_the_menu_bar_app():
    note = dc.ollama_restart_note("darwin")
    assert "menu bar" in note and "Applications" in note
    assert "brew services" in note, "a Homebrew service has no menu bar icon to quit"


def test_the_variable_needs_launchctl_on_a_mac():
    note = dc.ollama_variable_note("darwin")
    assert "launchctl setenv OLLAMA_NO_CLOUD 1" in note
    assert "launchctl" not in dc.ollama_variable_note("win32")
    assert "launchctl" not in dc.ollama_variable_note("linux")


def test_nobody_there_to_answer_is_told_about_launchctl_on_a_mac(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(dc.sys, "platform", "darwin")
    assert dc.apply_cloud_step(tmp_path / "server.json", assume_yes=False, interactive=False) is False
    out = capsys.readouterr().out
    assert "python3 dc.py setup --disable-ollama-cloud" in out
    assert "launchctl setenv OLLAMA_NO_CLOUD 1" in out


SYNCED = [
    ("/Users/a/Library/Mobile Documents/com~apple~CloudDocs/dc", "iCloud Drive"),
    ("/Users/a/Library/CloudStorage/Dropbox/dc", "cloud storage"),
    ("/Users/a/Desktop/dc", "Desktop"),
    ("/Users/a/Documents/dc", "Desktop or Documents"),
    ("/Users/A/DOCUMENTS/dc", "Desktop or Documents"),
    ("/Users/a/OneDrive - City/dc", "OneDrive"),
]


@pytest.mark.parametrize("path,service", SYNCED)
def test_doctor_flags_what_the_gateway_refuses_on_a_mac(path, service):
    found = dc.sync_service(PurePosixPath(path), {}, "darwin", "/Users/a")
    assert found is not None and service in found


@pytest.mark.parametrize(
    "path",
    [
        "/Users/a/Library/Application Support/DigitalCommons",
        "/Users/b/Documents/dc",
        "/opt/Documents/dc",
    ],
)
def test_doctor_accepts_ordinary_mac_folders(path):
    assert dc.sync_service(PurePosixPath(path), {}, "darwin", "/Users/a") is None


def test_mac_folder_rules_do_not_apply_elsewhere():
    assert dc.sync_service(PurePosixPath("/home/a/Documents/dc"), {}, "linux", "/home/a") is None


def test_onedrive_is_found_in_windows_paths_on_any_computer():
    path = PureWindowsPath(r"C:\Users\A\OneDrive - City\dc")
    assert dc.sync_service(path, {}, "win32", r"C:\Users\A") == "OneDrive"
    root = r"C:\Corporate Sync"
    assert dc.sync_service(PureWindowsPath(root, "dc"), {"OneDriveCommercial": root}, "win32", "C:\\") == "OneDrive"


@pytest.mark.parametrize(
    "path,platform",
    [(path, "darwin") for path, _ in SYNCED]
    + [("/Users/a/Library/Application Support/DigitalCommons", "darwin"), ("/home/a/Documents/dc", "linux")],
)
def test_doctor_and_the_gateway_agree(path, platform):
    from app.security import paths

    ours = dc.sync_service(PurePosixPath(path), {}, platform, "/Users/a")
    assert ours == paths.sync_service(PurePosixPath(path), {}, platform, "/Users/a")


def test_doctor_follows_a_link_into_a_synced_folder(tmp_path):
    target = tmp_path / "OneDrive" / "data"
    target.mkdir(parents=True)
    link = tmp_path / "plain"
    try:
        link.symlink_to(target, target_is_directory=True)
    except OSError:
        pytest.skip("symlinks need extra rights on this machine")
    assert dc.data_folder_problem(link, {}, "linux", tmp_path) == "OneDrive"


def fake_popen(seen):
    class Fake:
        pid = 4242

        def __init__(self, command, **options):
            seen.update(command=command, **options)

    return Fake


def test_every_posix_gateway_gets_a_lifeline_instead_of_a_parent_death_hook(tmp_path, monkeypatch):
    seen: dict = {}
    pipes = []
    real_pipe = os.pipe

    def pipe():
        pipes.append(real_pipe())
        return pipes[-1]

    monkeypatch.setattr(dc, "WINDOWS", False)
    monkeypatch.setattr(dc, "require_venv", lambda: tmp_path / "python")
    monkeypatch.setattr(dc.os, "pipe", pipe)
    monkeypatch.setattr(dc.subprocess, "Popen", fake_popen(seen))
    dc.spawn_gateway({"DC_DATA_DIR": "/d"}, None)

    reader, writer = pipes[0]
    assert seen["pass_fds"] == (reader,)
    assert seen["env"][dc.LIFELINE_VARIABLE] == str(reader)
    assert "preexec_fn" not in seen, "a preexec hook is not safe once threads exist"
    with pytest.raises(OSError):
        os.fstat(reader)
    os.fstat(writer)
    os.close(writer)


def test_the_gateway_knows_the_same_variable():
    from app.lifeline import LIFELINE_VARIABLE

    assert LIFELINE_VARIABLE == dc.LIFELINE_VARIABLE


def test_a_record_with_no_process_never_signals_anything(monkeypatch):
    """On POSIX, os.kill(0, ...) signals this whole process group."""

    def refuse(pid):
        pytest.fail(f"pid {pid} was signalled")

    monkeypatch.setattr(dc, "_ask_to_exit", refuse)
    monkeypatch.setattr(dc, "_force_stop", refuse)
    assert dc.stop_process(dc.GatewayRecord(0, None, "dev")) is True
    assert dc.stop_process(dc.GatewayRecord(-1, None, "dev")) is True


def test_an_interrupt_before_the_record_exists_still_stops_the_gateway(tmp_path, monkeypatch):
    stopped = []

    class Process:
        pid = 4242

    def interrupted(pid):
        raise KeyboardInterrupt

    monkeypatch.setattr(dc, "ensure_terminal_built", lambda: None)
    monkeypatch.setattr(dc, "existing_gateway", lambda *args: None)
    monkeypatch.setattr(dc, "require_venv", lambda: tmp_path / "python")
    monkeypatch.setattr(dc, "spawn_gateway", lambda env, log: Process())
    monkeypatch.setattr(dc, "process_start_time", interrupted)
    monkeypatch.setattr(dc, "stop_process", lambda record, process: stopped.append(record.pid))
    assert dc.run_session("dev") == 130
    assert stopped == [4242]


def test_ps_is_asked_in_a_fixed_locale(monkeypatch):
    seen = {}

    def run(command, **options):
        seen.update(options)
        return subprocess.CompletedProcess(command, 0, stdout="Mon Nov  2 06:40:01 2026\n")

    monkeypatch.setattr(dc, "WINDOWS", False)
    monkeypatch.setattr(dc.subprocess, "run", run)
    assert dc.process_start_time(4242) == "Mon Nov  2 06:40:01 2026"
    assert seen["env"]["LC_ALL"] == "C"


def fake_build(calls):
    def run(command, cwd=None, env=None):
        calls.append(env)
        dc.Path(command[command.index("-o") + 1]).write_bytes(b"binary")
        return subprocess.CompletedProcess(command, 0)

    return run


def test_build_all_makes_each_target_with_cgo_off(tmp_path, monkeypatch, capsys):
    calls: list = []
    monkeypatch.setenv("CGO_ENABLED", "1")
    monkeypatch.setattr(dc, "DIST", tmp_path / "dist")
    monkeypatch.setattr(dc, "find_go", lambda *args: dc.Path("go"))
    monkeypatch.setattr(dc.subprocess, "run", fake_build(calls))
    assert dc.main(["build", "--all"]) == 0

    assert [(env["GOOS"], env["GOARCH"], env["CGO_ENABLED"]) for env in calls] == [
        ("darwin", "arm64", "0"),
        ("darwin", "amd64", "0"),
        ("windows", "amd64", "0"),
    ]
    assert sorted(path.name for path in (tmp_path / "dist").iterdir()) == [
        "digital-commons-macos-amd64",
        "digital-commons-macos-arm64",
        "digital-commons-windows-amd64.exe",
    ]


def test_build_all_says_it_is_the_terminal_only(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(dc, "DIST", tmp_path / "dist")
    monkeypatch.setattr(dc, "find_go", lambda *args: dc.Path("go"))
    monkeypatch.setattr(dc.subprocess, "run", fake_build([]))
    dc.main(["build", "--all"])
    out = capsys.readouterr().out
    assert "terminal only" in out
    assert "set up on the Mac itself" in out
    assert "python3 dc.py setup" in out, "this command is run on the Mac, even when built on Windows"


def test_a_failed_target_stops_the_build(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(dc, "DIST", tmp_path / "dist")
    monkeypatch.setattr(dc, "find_go", lambda *args: dc.Path("go"))
    monkeypatch.setattr(dc.subprocess, "run", lambda *a, **k: subprocess.CompletedProcess(a, 2))
    assert dc.main(["build", "--all"]) == 1
    assert "build failed (exit 2)" in capsys.readouterr().err


def test_a_plain_build_still_goes_to_bin(tmp_path, monkeypatch):
    calls: list = []
    monkeypatch.setattr(dc, "BIN", tmp_path / "bin")
    monkeypatch.setattr(dc, "BINARY", tmp_path / "bin" / "digital-commons")
    monkeypatch.setattr(dc, "find_go", lambda *args: dc.Path("go"))
    monkeypatch.setattr(dc.subprocess, "run", fake_build(calls))
    assert dc.main(["build"]) == 0
    assert "GOOS" not in calls[0] or calls[0]["GOOS"] == os.environ.get("GOOS")
    assert (tmp_path / "bin" / "digital-commons").is_file()
