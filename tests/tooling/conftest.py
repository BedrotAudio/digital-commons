"""Shared setup for the tooling tests.

Nothing here may reach the real home folder or the real data folder, because
several tests exercise code that writes ~/.ollama/server.json.
"""

from __future__ import annotations

import socket
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "backend"))


@pytest.fixture(autouse=True)
def isolated_home(tmp_path, monkeypatch):
    """Point every way of finding the home folder, and the data folder, at tmp_path."""
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("USERPROFILE", str(home))
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    for name in ("DC_CONFIG", "DC_MODELS_CONFIG", "DC_PROVIDER", "DC_GATEWAY_TOKEN"):
        monkeypatch.delenv(name, raising=False)
    return home


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]
