"""Starting and stopping a real gateway through dc.py, with the mock provider.

These need backend/.venv, so they are skipped until `dc.py setup` has run. Each
test uses its own port and data folder, and never touches the real ones.
"""

from __future__ import annotations

import os
import re
import signal
import subprocess
import sys
import time
from types import SimpleNamespace

import pytest
from conftest import ROOT, free_port

import dc

pytestmark = pytest.mark.skipif(
    not dc.venv_python().is_file(), reason="backend/.venv is missing: run dc.py setup"
)

START_TIMEOUT_S = 60
WINDOWS = sys.platform == "win32"


@pytest.fixture
def rig(tmp_path):
    port = free_port()
    shipped = (ROOT / "config" / "development.yaml").read_text(encoding="utf-8")
    config = tmp_path / "development.yaml"
    config.write_text(re.sub(r"(?m)^(\s*port:\s*)8000\b", rf"\g<1>{port}", shipped, count=1), encoding="utf-8")
    data = tmp_path / "data"
    env = {
        **os.environ,
        "DC_CONFIG": str(config),
        "DC_DATA_DIR": str(data),
        "DC_PROVIDER": "mock",
    }
    return SimpleNamespace(env=env, data=data, port=port, url=f"http://127.0.0.1:{port}")


def wait_until(condition, seconds):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if condition():
            return True
        time.sleep(0.2)
    return condition()


def start_dc_backend(rig, tmp_path):
    """Run `dc.py backend` as a person would, in its own process group."""
    output = open(tmp_path / "dc-output.txt", "wb")
    flags = subprocess.CREATE_NEW_PROCESS_GROUP if WINDOWS else 0
    process = subprocess.Popen(
        [sys.executable, str(ROOT / "dc.py"), "backend"],
        env=rig.env,
        cwd=ROOT,
        stdin=subprocess.DEVNULL,
        stdout=output,
        stderr=subprocess.STDOUT,
        creationflags=flags,
    )
    process.output_file = output
    assert wait_until(lambda: dc.gateway_healthy(rig.url), START_TIMEOUT_S), (
        "the gateway did not start:\n" + (tmp_path / "dc-output.txt").read_text(errors="replace")
    )
    return process


def finish(process):
    if process.poll() is None:
        process.kill()
        process.wait(timeout=10)
    process.output_file.close()


def test_the_gateway_starts_answers_and_stops_gracefully(rig):
    settings = dc.load_settings(rig.env)
    assert settings.port == rig.port
    log = rig.data / "gateway.log"
    process = dc.spawn_gateway(rig.env, log)
    record = dc.GatewayRecord(process.pid, dc.process_start_time(process.pid), "dev", str(log))
    try:
        dc.wait_for_gateway(rig.url, process, log, timeout=START_TIMEOUT_S)
        assert dc.gateway_healthy(rig.url)
        assert dc.process_start_time(record.pid) == record.started
        assert dc.stop_process(record, process) is True, "it had to be forced"
    finally:
        if process.poll() is None:
            process.kill()
    assert not dc.gateway_healthy(rig.url)
    assert "Application shutdown complete" in log.read_text(encoding="utf-8", errors="replace")


def test_a_gateway_that_cannot_start_is_reported_with_its_log(rig, tmp_path):
    env = {**rig.env, "DC_DATA_DIR": "relative-is-refused"}
    log = tmp_path / "failed.log"
    process = dc.spawn_gateway(env, log)
    with pytest.raises(dc.ToolError) as caught:
        dc.wait_for_gateway(rig.url, process, log, timeout=START_TIMEOUT_S)
    assert "stopped during startup" in str(caught.value)
    assert "absolute path" in str(caught.value)


def test_ctrl_c_in_dc_backend_stops_the_gateway(rig, tmp_path):
    process = start_dc_backend(rig, tmp_path)
    try:
        record = dc.running_record(rig.data)
        assert record is not None and record.mode == "backend"
        process.send_signal(signal.CTRL_BREAK_EVENT if WINDOWS else signal.SIGINT)
        process.wait(timeout=30)
    finally:
        finish(process)
    assert not dc.gateway_healthy(rig.url)
    assert dc.read_record(rig.data) is None
    assert "Stopping the gateway" in (tmp_path / "dc-output.txt").read_text(errors="replace")


def test_dc_stop_stops_a_gateway_started_elsewhere(rig, tmp_path):
    process = start_dc_backend(rig, tmp_path)
    try:
        result = subprocess.run(
            [sys.executable, str(ROOT / "dc.py"), "stop"],
            env=rig.env,
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=60,
        )
        assert result.returncode == 0, result.stdout + result.stderr
        assert "Gateway stopped." in result.stdout
        process.wait(timeout=30)
    finally:
        finish(process)
    assert not dc.gateway_healthy(rig.url)
    assert dc.read_record(rig.data) is None


def test_dc_stop_with_nothing_running(rig):
    result = subprocess.run(
        [sys.executable, str(ROOT / "dc.py"), "stop"],
        env=rig.env,
        cwd=ROOT,
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0
    assert "No gateway is running." in result.stdout


def test_a_second_backend_is_refused(rig, tmp_path):
    process = start_dc_backend(rig, tmp_path)
    try:
        second = subprocess.run(
            [sys.executable, str(ROOT / "dc.py"), "backend"],
            env=rig.env,
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=60,
        )
        assert second.returncode == 1
        assert "already running" in second.stderr
        assert dc.gateway_healthy(rig.url)
    finally:
        finish(process)
        record = dc.running_record(rig.data)
        if record is not None:
            dc.stop_process(record)


def test_the_gateway_does_not_outlive_a_killed_dc(rig, tmp_path):
    process = start_dc_backend(rig, tmp_path)
    record = dc.running_record(rig.data)
    assert record is not None
    try:
        process.kill()
        process.wait(timeout=10)
        assert wait_until(lambda: not dc.gateway_healthy(rig.url), 30), "the gateway was orphaned"
    finally:
        finish(process)
        if dc.process_start_time(record.pid) == record.started:
            dc.stop_process(record)
