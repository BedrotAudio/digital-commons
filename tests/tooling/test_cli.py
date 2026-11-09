"""dc.py as a person runs it."""

from __future__ import annotations

import os
import re
import subprocess
import sys

import pytest
from conftest import ROOT, free_port

import dc

COMMANDS = ["setup", "start", "backend", "frontend", "dev", "demo", "test", "build", "stop", "clean", "doctor"]


def run(*args, env=None):
    return subprocess.run(
        [sys.executable, str(ROOT / "dc.py"), *args],
        cwd=ROOT,
        env=env,
        capture_output=True,
        text=True,
        timeout=120,
    )


def test_help_lists_every_command():
    result = run("--help")
    assert result.returncode == 0
    for name in COMMANDS:
        assert name in result.stdout


def test_no_arguments_prints_help():
    result = run()
    assert result.returncode == 0
    assert "commands" in result.stdout


def test_an_unknown_command_fails_with_usage():
    result = run("frobnicate")
    assert result.returncode == 2
    assert "invalid choice" in result.stderr


def test_every_command_has_its_own_help():
    for name in COMMANDS:
        assert run(name, "--help").returncode == 0


def test_setup_offers_the_cloud_flag_and_clean_offers_all():
    assert "--disable-ollama-cloud" in run("setup", "--help").stdout
    assert "--all" in run("clean", "--help").stdout


def test_the_parser_and_the_command_table_agree():
    assert sorted(name for name, _, _ in dc.COMMANDS) == sorted(COMMANDS)


def test_doctor_prints_a_report_and_a_count(tmp_path):
    port = free_port()
    shipped = (ROOT / "config" / "development.yaml").read_text(encoding="utf-8")
    config = tmp_path / "development.yaml"
    config.write_text(re.sub(r"(?m)^(\s*port:\s*)8000\b", rf"\g<1>{port}", shipped, count=1), encoding="utf-8")
    env = {
        **os.environ,
        "DC_CONFIG": str(config),
        "DC_PROVIDER": "mock",
        "DC_DATA_DIR": str(tmp_path / "data"),
    }
    result = run("doctor", env=env)
    assert result.returncode in (0, 1)
    for label in ("Python", "Go", "Environment", "Terminal", "Gateway", "Data folder"):
        assert label in result.stdout
    assert re.search(r"Provider\s+mock", result.stdout)
    assert f"Port {port}" in result.stdout
    assert "problem(s) found." in result.stdout or "No problems found." in result.stdout


def test_setup_rebuilds_an_environment_left_without_pip(tmp_path, capsys):
    """A failed or interrupted create leaves python but no pip. Setup used to reuse it
    and fail on every run after, even once the cause was fixed."""
    venv = tmp_path / "venv"
    subprocess.run([sys.executable, "-m", "venv", "--without-pip", str(venv)], check=True, timeout=120)
    python = dc.venv_python(venv)
    assert python.is_file() and not dc.venv_has_pip(python)

    assert dc.create_venv(venv) == python
    assert "incomplete, so it is being rebuilt" in capsys.readouterr().out
    assert dc.venv_has_pip(python)

    dc.create_venv(venv)
    assert "Using the existing virtual environment" in capsys.readouterr().out


@pytest.mark.parametrize("name", ["setup.sh", "start.sh", "stop.sh"])
def test_shell_wrappers_delegate_to_dc_py(name):
    text = (ROOT / "scripts" / name).read_text(encoding="utf-8")
    assert text.startswith("#!/bin/sh\n")
    assert "\r" not in text
    assert 'dc.py"' in text


def test_the_makefile_covers_the_documented_targets():
    text = (ROOT / "Makefile").read_text(encoding="utf-8")
    for target in ("setup", "backend", "frontend", "dev", "demo", "test", "clean", "stop", "doctor", "build"):
        assert f"\n{target}:\n\t$(PYTHON) dc.py {target}\n" in text


def test_dc_cmd_runs_dc_py_and_passes_arguments_on():
    text = (ROOT / "dc.cmd").read_text(encoding="utf-8")
    assert '"%DC_SCRIPT%" %*' in text
    assert "exit /b %ERRORLEVEL%" in text
