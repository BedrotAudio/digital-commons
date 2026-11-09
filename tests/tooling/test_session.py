"""The dev and demo commands: a real gateway, with a stand-in for the terminal.

The terminal needs a console, so run_terminal is replaced by a function that records
what the real one would have been given and what the gateway looked like at that
moment. Everything else is the real code path, including the gateway process.
"""

from __future__ import annotations

import os
import re
import time
from pathlib import Path

import pytest
from conftest import ROOT, free_port

import dc

pytestmark = pytest.mark.skipif(
    not dc.venv_python().is_file(), reason="backend/.venv is missing: run dc.py setup"
)


@pytest.fixture
def session(tmp_path, monkeypatch):
    port = free_port()
    shipped = (ROOT / "config" / "development.yaml").read_text(encoding="utf-8")
    config = tmp_path / "development.yaml"
    config.write_text(re.sub(r"(?m)^(\s*port:\s*)8000\b", rf"\g<1>{port}", shipped, count=1), encoding="utf-8")
    data = tmp_path / "data"
    monkeypatch.setenv("DC_CONFIG", str(config))
    monkeypatch.setenv("DC_DATA_DIR", str(data))
    monkeypatch.setattr(dc, "ensure_terminal_built", lambda: None)

    seen: dict = {}

    def stand_in(env, extra=(), config=dc.TERMINAL_CONFIG):
        seen["env"] = dict(env)
        seen["extra"] = list(extra)
        seen["config"] = config
        seen["healthy"] = dc.gateway_healthy(url)
        seen["record"] = dc.read_record(data)
        seen["token"] = Path(env["DC_DATA_DIR"], "local-token").is_file()
        action = seen.get("action")
        if action is not None:
            action()
        return seen.get("code", 7)

    url = f"http://127.0.0.1:{port}"
    monkeypatch.setattr(dc, "run_terminal", stand_in)
    seen["url"] = url
    seen["data"] = data
    return seen


def gone(pid: int, started) -> bool:
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if dc.process_start_time(pid) != started:
            return True
        time.sleep(0.1)
    return False


def test_dev_runs_the_terminal_against_a_started_gateway_then_stops_it(session):
    assert dc.run_session("dev") == 7

    assert session["healthy"], "the gateway must answer before the terminal starts"
    assert session["token"], "the terminal must be able to find the gateway's token"
    assert session["extra"] == ["--gateway", session["url"]]
    record = session["record"]
    assert record is not None and record.mode == "dev"
    assert Path(session["env"]["DC_DATA_DIR"]) == session["data"]
    assert "DC_PROVIDER" not in session["env"] or session["env"]["DC_PROVIDER"] != "mock"

    assert not dc.gateway_healthy(session["url"]), "the gateway must stop with the terminal"
    assert gone(record.pid, record.started)
    assert not dc.pid_file(session["data"]).exists()
    assert (session["data"] / "gateway.log").is_file()


def test_demo_keeps_its_own_folder_and_uses_the_mock_provider(session):
    assert dc.run_session("demo") == 7

    demo = session["data"] / "demo"
    assert Path(session["env"]["DC_DATA_DIR"]) == demo
    assert session["env"]["DC_PROVIDER"] == "mock"
    assert session["healthy"] and session["token"]
    assert session["record"].mode == "demo"
    assert (demo / "gateway.log").is_file()
    assert (demo / "conversations.db").is_file()
    assert not (session["data"] / "conversations.db").exists(), "real conversations must not be touched"
    assert not dc.gateway_healthy(session["url"])
    assert not dc.pid_file(session["data"]).exists()


def test_ctrl_c_in_the_terminal_still_stops_the_gateway(session):
    def interrupt():
        raise KeyboardInterrupt

    session["action"] = interrupt
    assert dc.run_session("dev") == 130
    assert not dc.gateway_healthy(session["url"])
    assert not dc.pid_file(session["data"]).exists()
    record = session["record"]
    assert gone(record.pid, record.started)


def test_a_crash_in_the_terminal_still_stops_the_gateway(session):
    def crash():
        raise OSError("the terminal could not be started")

    session["action"] = crash
    with pytest.raises(OSError):
        dc.run_session("dev")
    assert not dc.gateway_healthy(session["url"])
    assert not dc.pid_file(session["data"]).exists()


def test_a_gateway_that_is_already_running_is_used_and_left_running(session):
    settings = dc.load_settings(os.environ)
    data = session["data"]
    env = dc.session_env(settings, data)
    env["DC_PROVIDER"] = "mock"
    process = dc.spawn_gateway(env, data / "gateway.log")
    record = dc.GatewayRecord(process.pid, dc.process_start_time(process.pid), "backend", None)
    dc.write_record(data, record)
    try:
        dc.wait_for_gateway(session["url"], process, data / "gateway.log")
        assert dc.run_session("dev") == 7
        assert session["healthy"]
        assert dc.gateway_healthy(session["url"]), "dev must not stop a gateway it did not start"
        assert dc.running_record(data) is not None
        assert dc.read_record(data).pid == process.pid
    finally:
        dc.stop_process(record, process)
        dc.clear_record(data)


def test_demo_will_not_borrow_a_gateway_that_holds_real_conversations(session):
    settings = dc.load_settings(os.environ)
    data = session["data"]
    env = dc.session_env(settings, data)
    env["DC_PROVIDER"] = "mock"
    process = dc.spawn_gateway(env, data / "gateway.log")
    record = dc.GatewayRecord(process.pid, dc.process_start_time(process.pid), "dev", None)
    dc.write_record(data, record)
    try:
        dc.wait_for_gateway(session["url"], process, data / "gateway.log")
        with pytest.raises(dc.ToolError, match="not a demo gateway"):
            dc.run_session("demo")
        assert dc.gateway_healthy(session["url"])
    finally:
        dc.stop_process(record, process)
        dc.clear_record(data)


def test_dev_will_not_borrow_a_demo_gateway(session):
    settings = dc.load_settings(os.environ)
    data = session["data"]
    env = dc.session_env(settings, data / "demo")
    env["DC_PROVIDER"] = "mock"
    process = dc.spawn_gateway(env, data / "demo" / "gateway.log")
    record = dc.GatewayRecord(process.pid, dc.process_start_time(process.pid), "demo", None)
    dc.write_record(data, record)
    try:
        dc.wait_for_gateway(session["url"], process, data / "demo" / "gateway.log")
        with pytest.raises(dc.ToolError, match="demo gateway is running"):
            dc.run_session("dev")
    finally:
        dc.stop_process(record, process)
        dc.clear_record(data)


def test_dev_gives_the_terminal_the_project_settings(session):
    dc.run_session("dev")
    assert session["config"] == dc.TERMINAL_CONFIG
