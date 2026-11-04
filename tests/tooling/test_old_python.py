"""dc.py on the old python3 that macOS ships, which must end in a sentence, not a traceback.

No old Python is installed where these run, so the version is injected, and parsing is
checked separately because a syntax error would stop the file before any line runs.
"""

from __future__ import annotations

import ast
import io
import os
import subprocess
import sys
import tokenize

import pytest
from conftest import ROOT

import dc

SOURCE = (ROOT / "dc.py").read_text(encoding="utf-8")

# shutil loads a Windows-only module on a patched platform, so it comes first.
GATE = """
import runpy, shutil, sys
sys.version_info = (3, 9, 6, "final", 0)
sys.platform = {platform!r}
sys.argv = ["dc.py", "setup"]
runpy.run_path({path!r}, run_name="__main__")
"""


def run_on_an_old_python(platform):
    """DC_PYTHON_SWITCHED stops the gate handing over to a real interpreter here."""
    return subprocess.run(
        [sys.executable, "-c", GATE.format(platform=platform, path=str(ROOT / "dc.py"))],
        env={**os.environ, dc.PYTHON_SWITCHED: "1"},
        capture_output=True,
        text=True,
        timeout=60,
    )


@pytest.mark.parametrize(
    "platform,instruction",
    [
        ("darwin", "brew install python@3.12"),
        ("win32", "winget install Python.Python.3.12"),
        ("linux", "sudo apt install python3.12 python3.12-venv"),
    ],
)
def test_an_old_python_gets_an_instruction_not_a_traceback(platform, instruction):
    result = run_on_an_old_python(platform)
    assert result.returncode == 1
    assert "Traceback" not in result.stderr
    assert "needs Python 3.11 or newer. This is Python 3.9." in result.stderr
    assert instruction in result.stderr


def test_nothing_before_the_check_imports_more_than_the_oldest_python_has():
    imported = set()
    for node in ast.parse(SOURCE).body:
        if isinstance(node, ast.If) and "version_info" in ast.dump(node.test):
            break
        if isinstance(node, ast.Import):
            imported.update(alias.name for alias in node.names)
        if isinstance(node, ast.ImportFrom):
            imported.add(node.module)
    else:
        pytest.fail("the version check was not found")
    assert imported == {"__future__", "os", "shutil", "sys"}


def test_the_whole_file_parses_on_python_3_8():
    ast.parse(SOURCE, feature_version=(3, 8))


@pytest.mark.skipif(sys.version_info < (3, 12), reason="only 3.12 tokenizes inside an f-string")
def test_no_f_string_needs_python_3_12():
    """Before 3.12 an f-string could not reuse its own quote or hold a backslash."""
    quotes = []
    for token in tokenize.generate_tokens(io.StringIO(SOURCE).readline):
        if token.type == tokenize.FSTRING_START:
            quote = token.string.lstrip("rRbBfFuU")[0]
            assert quote not in quotes, f"line {token.start[0]} reuses the quote of the f-string around it"
            quotes.append(quote)
        elif token.type == tokenize.FSTRING_END:
            quotes.pop()
        elif quotes and token.type == tokenize.STRING:
            assert token.string.lstrip("rRbBuU")[0] not in quotes, f"line {token.start[0]}"
            assert "\\" not in token.string, f"line {token.start[0]} has a backslash inside an f-string"


def test_a_new_interpreter_is_found_when_python3_still_points_at_the_old_one(tmp_path):
    installed = {"python3.13": "/opt/homebrew/bin/python3.13", "python3.12": "/opt/homebrew/bin/python3.12"}
    assert dc.newer_python(str(tmp_path), which=installed.get) == "/opt/homebrew/bin/python3.12"
    assert dc.newer_python(str(tmp_path), which=lambda name: None) is None


def test_the_project_environment_is_preferred_once_it_exists(tmp_path):
    venv = tmp_path / "backend" / ".venv" / "bin"
    venv.mkdir(parents=True)
    (venv / "python").write_bytes(b"")
    found = dc.newer_python(str(tmp_path), which=lambda name: "/opt/homebrew/bin/python3.12")
    assert found == str(venv / "python")
