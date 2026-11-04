"""The files GitHub runs. A mistake here only shows up after a push, so it is checked here."""

from __future__ import annotations

import importlib.util
import os
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from types import SimpleNamespace

import pytest
import yaml
from conftest import ROOT, free_port
from test_ollama_flow import FakeOllama

import dc

WORKFLOW = ROOT / ".github" / "workflows" / "tests.yml"
TINY = ROOT / "tests" / "ci" / "models-tiny.yaml"


def load_live_check():
    spec = importlib.util.spec_from_file_location("live_check", ROOT / "tests" / "ci" / "live_check.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


live_check = load_live_check()


@pytest.fixture(scope="module")
def workflow():
    return yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))


def steps(job):
    return [step.get("run", "") for step in job["steps"]]


def test_every_action_is_pinned_to_one_exact_commit():
    used = re.findall(r"uses:\s*(\S+)", WORKFLOW.read_text(encoding="utf-8"))
    assert used
    for action in used:
        assert re.fullmatch(r"[\w./-]+@[0-9a-f]{40}", action), action
    versions: dict = {}
    for action in used:
        name, commit = action.split("@")
        versions.setdefault(name, set()).add(commit)
    assert all(len(commits) == 1 for commits in versions.values()), versions


def test_permissions_are_read_only(workflow):
    assert workflow["permissions"] == {"contents": "read"}
    assert all("permissions" not in job for job in workflow["jobs"].values())


def test_every_platform_runs_the_tests_with_its_own_python(workflow):
    rows = workflow["jobs"]["test"]["strategy"]["matrix"]["include"]
    interpreters = {row["runner"]: row["python"] for row in rows}
    assert interpreters == {
        "macos-latest": "python3",
        "macos-26-intel": "python3",
        "windows-latest": "python",
        "ubuntu-latest": "python3",
    }


def test_the_harness_runs_with_e2e_switched_on(workflow):
    step = next(step for step in workflow["jobs"]["test"]["steps"] if "internal/e2e" in step.get("run", ""))
    assert step["env"]["DC_E2E"] == "1"


def test_the_cloud_gate_job_checks_the_refusal_before_it_turns_cloud_access_off(workflow):
    job = workflow["jobs"]["mac-cloud-gate"]
    assert job["runs-on"] == "macos-latest"
    runs = steps(job)
    order = [
        "brew install ollama",
        "ollama serve",
        "ollama pull tinyllama",
        "dc.py setup < /dev/null",
        "dc.py backend",
        "tests/ci/live_check.py --cloud-on",
        "dc.py stop",
        "dc.py setup --disable-ollama-cloud",
        'kill "$(cat "$RUNNER_TEMP/ollama.pid")"',
        "ollama serve",
        "dc.py backend",
        "tests/ci/live_check.py",
    ]
    text = "\n".join(runs)
    place = -1
    for item in order:
        place = text.find(item, place + 1)
        assert place >= 0, f"{item!r} is missing or out of order"
    assert text.index("--disable-ollama-cloud") > text.index("--cloud-on")
    assert [run.strip() for run in runs if "live_check.py" in run] == [
        "python3 tests/ci/live_check.py --cloud-on --wait 120",
        "python3 tests/ci/live_check.py",
    ]
    assert "models-tiny.yaml" in text


def test_every_tier_uses_the_small_model():
    catalog = yaml.safe_load(TINY.read_text(encoding="utf-8"))["catalog"]["ollama"]
    assert {spec["name"] for spec in catalog.values()} == {"tinyllama"}
    assert set(catalog) == {"fast", "standard", "advanced"}
    assert not any(spec["think"] for spec in catalog.values())


def test_the_tiny_catalog_is_one_the_gateway_and_dc_py_accept(tmp_path):
    from app.config.settings import load_settings

    env = {"DC_MODELS_CONFIG": str(TINY), "DC_DATA_DIR": str(tmp_path / "data")}
    assert load_settings(env).provider_name == "ollama"
    assert load_settings({**env, "DC_PROVIDER": "mock"}).provider.kind == "mock"
    assert dc.load_settings(env).models == ("tinyllama",)


class Stream:
    def __init__(self, *blocks):
        self.lines = [line.encode() for block in blocks for line in (block + "\n\n").splitlines(True)]

    def __iter__(self):
        return iter(self.lines)


START = 'event: start\ndata: {"route": {"locality": "this_device"}}'
DELTA = 'event: delta\ndata: {"text": "Hi"}'
DONE = 'event: done\ndata: {"finish_reason": "stop"}'


def events(*blocks):
    return live_check.read_events(Stream(*blocks))


def test_a_good_stream_passes():
    live_check.check_chat(events(START, ": ping", DELTA, DONE), "this_device")


@pytest.mark.parametrize(
    "blocks,message",
    [
        ((DELTA, DONE), "begin with start"),
        ((START, DONE), "No text"),
        ((START, DELTA), "without done"),
        ((START, DELTA, 'event: error\ndata: {"code": "x"}'), "reported an error"),
    ],
)
def test_a_bad_stream_fails(blocks, message):
    with pytest.raises(live_check.CheckFailed, match=message):
        live_check.check_chat(events(*blocks), "this_device")


def test_a_chat_that_left_the_device_fails():
    with pytest.raises(live_check.CheckFailed, match="locality"):
        live_check.check_chat(events(START, DELTA, DONE), "demo")


def test_demo_is_a_flag_and_the_default_expects_a_real_model():
    assert live_check.parse_arguments(["--demo"]).demo is True
    assert live_check.parse_arguments([]).demo is False
    assert live_check.parse_arguments(["--cloud-on"]).cloud_on is True
    assert live_check.parse_arguments([]).cloud_on is False
    with pytest.raises(SystemExit):
        live_check.parse_arguments(["--demo", "--cloud-on"])


CLOSED_VIEW = {
    "state": "unavailable",
    "reason": "cloud_access_enabled",
    "locality": {"kind": "organization"},
    "inference": {"cloud_access_disabled": False},
}


def test_cloud_on_must_be_reported_by_ollama_itself():
    live_check.check_cloud_on({"cloud": {"disabled": False, "source": "none"}})
    for reply in ({"cloud": {"disabled": True}}, {"cloud": {"disabled": "false"}}, {}, [], None):
        with pytest.raises(live_check.CheckFailed, match="would prove nothing"):
            live_check.check_cloud_on(reply)


@pytest.mark.parametrize(
    "change,message",
    [
        ({"state": "ready", "reason": None}, "not unavailable"),
        ({"reason": "model_missing"}, "not unavailable"),
        ({"locality": {"kind": "this_device"}}, "this_device"),
        ({"inference": {"cloud_access_disabled": None}}, "not false"),
        ({"inference": None}, "not false"),
    ],
)
def test_a_status_that_does_not_refuse_fails(change, message):
    live_check.check_refused_status(CLOSED_VIEW)
    with pytest.raises(live_check.CheckFailed, match=message):
        live_check.check_refused_status({**CLOSED_VIEW, **change})


def test_only_a_policy_refusal_counts():
    live_check.check_refusal(503, {"error": {"code": "model_not_permitted"}})
    for code, body in ((503, {"error": {"code": "model_not_installed"}}), (502, {"error": {"code": "provider_error"}}), (503, {})):
        with pytest.raises(live_check.CheckFailed, match="not 503"):
            live_check.check_refusal(code, body)


def test_a_refusal_must_leave_nothing_behind():
    live_check.check_nothing_happened({"conversations": []}, {"models": []})
    with pytest.raises(live_check.CheckFailed, match="conversations behind"):
        live_check.check_nothing_happened({"conversations": [{"id": "c"}]}, {"models": []})
    with pytest.raises(live_check.CheckFailed, match="model loaded"):
        live_check.check_nothing_happened({"conversations": []}, {"models": [{"name": "tinyllama:latest"}]})


class StreamingGateway:
    """Accepts every chat, which is what a broken gate would look like from outside."""

    def __init__(self):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", 0)))
                payload = "\n\n".join((START, DELTA, DONE)).encode() + b"\n\n"
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


def test_a_chat_that_is_accepted_fails_the_refusal_check():
    gateway = StreamingGateway()
    try:
        with pytest.raises(live_check.CheckFailed, match="accepted while cloud access is on"):
            live_check.send_refused_chat(gateway.url, "token")
    finally:
        gateway.close()


@pytest.fixture
def gateway_on_fake_ollama(tmp_path):
    """A real gateway with the CI catalog, in front of a stand-in Ollama. Never the real one."""
    if not dc.venv_python().is_file():
        pytest.skip("backend/.venv is missing: run dc.py setup")
    ollama = FakeOllama(models=["tinyllama"])
    port = free_port()
    shipped = (ROOT / "config" / "development.yaml").read_text(encoding="utf-8")
    config = re.sub(r"(?m)^(\s*port:\s*)8000\b", rf"\g<1>{port}", shipped, count=1)
    config = config.replace("base_url: http://127.0.0.1:11434", f"base_url: {ollama.url}", 1)
    assert ollama.url in config
    (tmp_path / "development.yaml").write_text(config, encoding="utf-8")
    data = tmp_path / "gateway-data"
    env = {
        **os.environ,
        "DC_CONFIG": str(tmp_path / "development.yaml"),
        "DC_MODELS_CONFIG": str(TINY),
        "DC_DATA_DIR": str(data),
    }
    log = tmp_path / "gateway.log"
    process = dc.spawn_gateway(env, log)
    record = dc.GatewayRecord(process.pid, dc.process_start_time(process.pid), "dev", str(log))
    try:
        dc.wait_for_gateway(f"http://127.0.0.1:{port}", process, log, timeout=60)
        yield SimpleNamespace(ollama=ollama, url=f"http://127.0.0.1:{port}", data=data)
    finally:
        dc.stop_process(record, process)
        ollama.close()


def cloud_on_check(rig):
    return live_check.main(
        ["--cloud-on", "--url", rig.url, "--data-dir", str(rig.data), "--ollama-url", rig.ollama.url, "--wait", "60"]
    )


def test_the_cloud_on_check_passes_against_a_real_gateway(gateway_on_fake_ollama, capsys):
    assert cloud_on_check(gateway_on_fake_ollama) == 0, capsys.readouterr().err
    assert ("POST", "/api/chat") not in gateway_on_fake_ollama.ollama.requests
    assert "Live check passed." in capsys.readouterr().out


def test_the_cloud_on_check_fails_when_ollama_reports_cloud_off(gateway_on_fake_ollama, capsys):
    gateway_on_fake_ollama.ollama.cloud = {"disabled": True, "source": "config"}
    assert cloud_on_check(gateway_on_fake_ollama) == 1
    assert "would prove nothing" in capsys.readouterr().err


def test_no_data_folder_is_a_plain_failure(monkeypatch, capsys):
    monkeypatch.delenv("DC_DATA_DIR")
    assert live_check.main([]) == 1
    assert "Set DC_DATA_DIR" in capsys.readouterr().err
