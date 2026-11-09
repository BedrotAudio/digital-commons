"""setup's Ollama step and doctor, against a fake Ollama. The real one is never contacted."""

from __future__ import annotations

import argparse
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

import dc


class FakeOllama:
    """Serves the read-only endpoints dc.py uses, and records every request."""

    def __init__(self, cloud=None, models=("Gemma4:12b",), status=200):
        self.cloud = {"disabled": False, "source": "none"} if cloud is None else cloud
        self.models = list(models)
        self.status = status
        self.requests = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                outer.requests.append(("GET", self.path))
                routes = {
                    "/api/version": {"version": "0.0.test"},
                    "/api/status": {"cloud": outer.cloud},
                    "/api/tags": {"models": [{"name": name} for name in outer.models]},
                    "/api/ps": {"models": []},
                }
                body = routes.get(self.path)
                code = outer.status if self.path == "/api/status" else (200 if body else 404)
                payload = json.dumps(body or {}).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_POST(self):
                outer.requests.append(("POST", self.path))
                # Read the body first. Closing with it unread makes the operating system
                # reset the connection, which macOS often does before the client has
                # read the reply, so the gateway saw a dropped connection instead.
                self.rfile.read(int(self.headers.get("Content-Length", 0)))
                self.send_response(500)
                self.send_header("Content-Length", "0")
                self.end_headers()

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def fake():
    servers = []

    def make(**options):
        servers.append(FakeOllama(**options))
        return servers[-1]

    yield make
    for server in servers:
        server.close()


def settings_for(url, models=("Gemma4:12b",), provider="ollama"):
    return dc.Settings("127.0.0.1", 8000, provider, url, "local", None, tuple(models))


def run_setup_step(server, isolated_home, flag=False):
    args = argparse.Namespace(disable_ollama_cloud=flag)
    dc.check_ollama_for_setup(settings_for(server.url), args)
    return isolated_home / ".ollama" / "server.json"


def test_cloud_already_off_asks_nothing(fake, isolated_home, capsys):
    server = fake(cloud={"disabled": True, "source": "config"})
    target = run_setup_step(server, isolated_home)
    out = capsys.readouterr().out
    assert "cloud access is off" in out
    assert "Turn off" not in out
    assert not target.exists()


def test_cloud_on_without_a_person_makes_no_change(fake, isolated_home, capsys):
    server = fake()
    target = run_setup_step(server, isolated_home)
    out = capsys.readouterr().out
    assert "Ollama cloud access is on." in out
    assert "setup --disable-ollama-cloud" in out
    assert not target.exists()


def test_cloud_on_with_the_flag_merges_and_says_to_restart(fake, isolated_home, capsys):
    server = fake()
    target = run_setup_step(server, isolated_home, flag=True)
    assert json.loads(target.read_text(encoding="utf-8")) == {"disable_ollama_cloud": True}
    assert "must be restarted" in capsys.readouterr().out


def test_the_flag_keeps_existing_settings(fake, isolated_home):
    target = isolated_home / ".ollama" / "server.json"
    target.parent.mkdir()
    target.write_text('{"keep": 1}', encoding="utf-8")
    run_setup_step(fake(), isolated_home, flag=True)
    assert json.loads(target.read_text(encoding="utf-8")) == {"keep": 1, "disable_ollama_cloud": True}
    assert (target.parent / "server.json.bak").read_text(encoding="utf-8") == '{"keep": 1}'


def test_an_unreadable_status_is_not_acted_on(fake, isolated_home, capsys):
    server = fake(status=404)
    target = run_setup_step(server, isolated_home, flag=True)
    assert "did not report its cloud setting" in capsys.readouterr().out
    assert not target.exists()


def test_a_missing_model_gets_the_pull_command(fake, isolated_home, capsys):
    run_setup_step(fake(cloud={"disabled": True}, models=[]), isolated_home)
    assert "ollama pull Gemma4:12b" in capsys.readouterr().out


def test_an_installed_model_is_reported_once(fake, isolated_home, capsys):
    run_setup_step(fake(cloud={"disabled": True}, models=["gemma4:12B"]), isolated_home)
    assert "The configured model is installed: Gemma4:12b" in capsys.readouterr().out


def test_no_ollama_is_explained_and_changes_nothing(isolated_home, capsys):
    from conftest import free_port

    args = argparse.Namespace(disable_ollama_cloud=False)
    dc.check_ollama_for_setup(settings_for(f"http://127.0.0.1:{free_port()}"), args)
    assert "ollama.com/download" in capsys.readouterr().out
    assert not (isolated_home / ".ollama").exists()


def test_only_read_requests_reach_ollama(fake, isolated_home):
    server = fake(cloud={"disabled": True})
    run_setup_step(server, isolated_home)
    assert server.requests and all(method == "GET" for method, _ in server.requests)
    assert not any(path.startswith(("/api/chat", "/api/generate")) for _, path in server.requests)


def checks_by_label(monkeypatch, settings):
    monkeypatch.setattr(dc, "load_settings", lambda env: settings)
    return {check.label: check for check in dc.doctor_checks()}


def test_doctor_reports_a_good_ollama(fake, monkeypatch):
    server = fake(cloud={"disabled": True, "source": "env"})
    checks = checks_by_label(monkeypatch, settings_for(server.url))
    assert checks["Ollama"].status == "ok" and "0.0.test" in checks["Ollama"].detail
    assert checks["Model"].status == "ok"
    assert checks["Cloud access"].status == "ok" and "env" in checks["Cloud access"].detail
    assert all(method == "GET" for method, _ in server.requests)


def test_doctor_flags_cloud_access_that_is_on(fake, monkeypatch):
    checks = checks_by_label(monkeypatch, settings_for(fake().url))
    assert checks["Cloud access"].status == "FAIL"
    assert "dc.py setup" in checks["Cloud access"].detail


def test_doctor_flags_a_missing_model_with_the_command(fake, monkeypatch):
    server = fake(cloud={"disabled": True}, models=["other:1b"])
    checks = checks_by_label(monkeypatch, settings_for(server.url))
    assert checks["Model"].status == "FAIL"
    assert "ollama pull Gemma4:12b" in checks["Model"].detail


def test_doctor_does_not_fail_demo_mode_for_a_missing_ollama(monkeypatch):
    from conftest import free_port

    settings = settings_for(f"http://127.0.0.1:{free_port()}", provider="mock")
    checks = checks_by_label(monkeypatch, settings)
    assert checks["Ollama"].status == "info"
    assert checks["Provider"].detail == "mock"


def test_doctor_fails_an_ollama_provider_with_no_ollama(monkeypatch):
    from conftest import free_port

    settings = settings_for(f"http://127.0.0.1:{free_port()}")
    assert checks_by_label(monkeypatch, settings)["Ollama"].status == "FAIL"
