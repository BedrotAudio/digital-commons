"""`dc.py start`: first-start setup of Ollama and the model, against a stand-in Ollama.

Nothing here downloads, installs, starts or stops a real Ollama: every action on the
computer goes through OllamaOps, which these tests replace.
"""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

import dc

MODEL = "Gemma4:12b"


class StandInOllama:
    """Answers the routes `prepare` reads. `running` False means nothing answers."""

    def __init__(self):
        self.running = True
        self.cloud_disabled = True
        self.models = [MODEL]
        self.tags_fail = False
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                if not outer.running:
                    self.send_error(503)
                    return
                if self.path == "/api/version":
                    body = {"version": "0.40.13"}
                elif self.path == "/api/status":
                    body = {"cloud": {"disabled": outer.cloud_disabled, "source": "config"}}
                elif self.path == "/api/tags" and not outer.tags_fail:
                    body = {"models": [{"name": name, "model": name} for name in outer.models]}
                else:
                    self.send_error(500)
                    return
                data = json.dumps(body).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class FakeOps(dc.OllamaOps):
    """Records what prepare does, and makes the stand-in react as Ollama would."""

    def __init__(self, ollama: StandInOllama, server_json: Path, installed: bool = True):
        self.ollama = ollama
        self.server_json = server_json
        self.installed = installed
        self.calls: list[str] = []
        super().__init__(
            find=self._find,
            install=self._install,
            start=self._start,
            stop=self._stop,
            wait=lambda url, up, limit: self.ollama.running == up,
            pull=self._pull,
        )

    def _find(self):
        return Path("ollama app.exe") if self.installed else None

    def _install(self):
        self.calls.append("install")
        self.installed = True
        return Path("ollama app.exe")

    def _start(self, app):
        self.calls.append("start")
        self.ollama.running = True
        if self.server_json.is_file():
            self.ollama.cloud_disabled = json.loads(self.server_json.read_text())["disable_ollama_cloud"]

    def _stop(self):
        self.calls.append("stop")
        self.ollama.running = False

    def _pull(self, url, name):
        self.calls.append(f"pull {name}")
        self.ollama.models.append(name)


@pytest.fixture
def ollama():
    stand_in = StandInOllama()
    yield stand_in
    stand_in.close()


def settings_for(ollama, provider="ollama"):
    return dc.Settings(
        host="127.0.0.1", port=8000, provider=provider, ollama_url=ollama.url,
        identity_mode="local", data_dir=None, models=(MODEL,),
    )


def run(ollama, tmp_path, *, yes=False, interactive=True, answers=(), installed=True, provider="ollama"):
    server_json = tmp_path / ".ollama" / "server.json"
    ops = FakeOps(ollama, server_json, installed)
    replies = iter(answers)
    asked: list[str] = []

    def ask(question):
        asked.append(question)
        return next(replies)

    problems = dc.prepare(
        settings_for(ollama, provider), yes, interactive, ask=ask, ops=ops,
        platform="win32", server_json=server_json,
    )
    return problems, ops.calls, asked, server_json


def test_a_ready_computer_changes_nothing_and_asks_nothing(ollama, tmp_path):
    problems, calls, asked, server_json = run(ollama, tmp_path)
    assert problems == [] and calls == [] and asked == []
    assert not server_json.exists()


def test_everything_missing_is_set_up_in_order(ollama, tmp_path):
    ollama.running, ollama.cloud_disabled, ollama.models = False, False, []
    problems, calls, asked, server_json = run(ollama, tmp_path, answers=("", "y", "yes"), installed=False)
    assert problems == []
    assert calls == ["install", "start", "stop", "start", f"pull {MODEL}"]
    assert len(asked) == 3
    assert "Ollama Inc." in asked[0] and "1.7 GB" in asked[0]
    assert "cloud access" in asked[1]
    assert MODEL in asked[2]
    assert json.loads(server_json.read_text()) == {"disable_ollama_cloud": True}


def test_installed_but_stopped_ollama_is_started_without_asking(ollama, tmp_path):
    ollama.running = False
    problems, calls, asked, _ = run(ollama, tmp_path)
    assert problems == [] and calls == ["start"] and asked == []


def test_declining_the_install_stops_there(ollama, tmp_path):
    ollama.running = False
    problems, calls, asked, _ = run(ollama, tmp_path, answers=("n",), installed=False)
    assert calls == [] and len(asked) == 1
    assert "not installed" in problems[0]


@pytest.mark.parametrize("answer", ["n", "no", "N"])
def test_declining_the_cloud_change_leaves_the_file_alone(ollama, tmp_path, answer):
    ollama.cloud_disabled = False
    problems, calls, _, server_json = run(ollama, tmp_path, answers=(answer,))
    assert not server_json.exists() and calls == []
    assert any("cloud access is on" in problem for problem in problems)


def test_a_restart_that_does_not_take_effect_is_reported(ollama, tmp_path):
    ollama.cloud_disabled = False
    server_json = tmp_path / ".ollama" / "server.json"
    ops = FakeOps(ollama, server_json)
    ops.start = lambda app: setattr(ollama, "running", True)  # Ollama ignores the file.
    problems = dc.prepare(settings_for(ollama), True, True, ops=ops, platform="win32", server_json=server_json)
    assert any("restarted" in problem for problem in problems)


def test_nobody_to_answer_changes_nothing(ollama, tmp_path):
    ollama.running, ollama.cloud_disabled, ollama.models = False, False, []
    problems, calls, asked, server_json = run(ollama, tmp_path, interactive=False, installed=False)
    assert calls == [] and asked == [] and not server_json.exists()
    assert problems


def test_yes_in_advance_answers_every_question(ollama, tmp_path):
    ollama.cloud_disabled, ollama.models = False, []
    problems, calls, asked, server_json = run(ollama, tmp_path, yes=True, interactive=False)
    assert problems == [] and asked == []
    assert calls == ["stop", "start", f"pull {MODEL}"]
    assert server_json.is_file()


def test_declining_the_model_is_reported_plainly(ollama, tmp_path):
    ollama.models = []
    problems, calls, _, _ = run(ollama, tmp_path, answers=("n",))
    assert calls == []
    assert any(MODEL in problem and "not downloaded" in problem for problem in problems)


def test_a_failed_model_list_offers_no_download(ollama, tmp_path):
    ollama.tags_fail = True
    problems, calls, asked, _ = run(ollama, tmp_path)
    assert calls == [] and asked == []
    assert any("did not list" in problem for problem in problems)


def test_other_providers_need_no_ollama(ollama, tmp_path):
    ollama.running = False
    assert run(ollama, tmp_path, provider="mock")[:3] == ([], [], [])


def test_no_download_is_offered_where_there_is_no_installer(ollama, tmp_path):
    ollama.running = False
    server_json = tmp_path / "server.json"
    ops = FakeOps(ollama, server_json, installed=False)
    problems = dc.prepare(settings_for(ollama), True, True, ops=ops, platform="linux", server_json=server_json)
    assert ops.calls == [] and "ollama.com/download" in problems[0]


OLLAMA_SUBJECT = (
    "CN=Ollama Inc., O=Ollama Inc., L=Toronto, S=Ontario, C=CA, SERIALNUMBER=2713355, "
    "OID.2.5.4.15=Private Organization"
)


@pytest.mark.parametrize(
    "status,subject,expected",
    [
        ("Valid", OLLAMA_SUBJECT, True),
        ("Valid\r", OLLAMA_SUBJECT, True),
        ("NotSigned", "", False),
        ("HashMismatch", OLLAMA_SUBJECT, False),
        ("Valid", "CN=Ollama Inc. Ltd, O=Ollama Inc. Ltd", False),
        ("Valid", 'CN=Someone, O="Evil, CN=Ollama Inc., O=Ollama Inc."', False),
        ("Valid", "CN=Ollama Inc., O=Someone Else", False),
    ],
)
def test_only_ollama_inc_signatures_pass_on_windows(status, subject, expected):
    assert dc.windows_signer_ok(status, subject) is expected


MAC_DETAILS = """Executable=/tmp/Ollama.app/Contents/MacOS/Ollama
Identifier=com.electron.ollama
Authority=Developer ID Application: Ollama Inc. (ABCDE12345)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
"""


def test_only_ollama_developer_id_passes_on_a_mac():
    assert dc.mac_authority_ok(MAC_DETAILS)
    assert not dc.mac_authority_ok(MAC_DETAILS.replace("Ollama Inc.", "Someone Else"))
    assert not dc.mac_authority_ok("Signature=adhoc\nTeamIdentifier=not set\n")
    assert not dc.mac_authority_ok(MAC_DETAILS.replace("Apple Root CA", "Some Root"))


def test_pull_progress_reads_ollama_events():
    line, key = dc.pull_progress({"status": "pulling abc", "total": 8_000_000_000, "completed": 4_000_000_000})
    assert "50%" in line and "4.0 of 8.0 GB" in line and key == ("pulling abc", 5)
    assert dc.pull_progress({"status": "verifying sha256 digest"}) == ("  verifying sha256 digest", ("verifying sha256 digest", -1))
    assert "100%" in dc.pull_progress({"status": "x", "total": 10, "completed": 12})[0]


def test_the_terminal_settings_are_copied_once_and_then_kept(tmp_path):
    data = tmp_path / "data"
    first = dc.user_terminal_config(data)
    assert first == data / "terminal.yaml"
    assert first.read_text(encoding="utf-8") == dc.TERMINAL_CONFIG.read_text(encoding="utf-8")
    first.write_text("application:\n  default_mode: developer\n", encoding="utf-8")
    assert dc.user_terminal_config(data).read_text(encoding="utf-8").endswith("developer\n")


def test_an_installed_copy_uses_its_private_python(tmp_path, monkeypatch):
    runtime = tmp_path / "runtime"
    python = dc.runtime_python(runtime)
    python.parent.mkdir(parents=True)
    python.write_text("")
    monkeypatch.setattr(dc, "RUNTIME", runtime)
    assert dc.require_venv() == python
    python.unlink()
    with pytest.raises(dc.ToolError, match="Reinstall"):
        dc.require_venv()


def test_the_runtime_layout_matches_python_build_standalone(tmp_path):
    assert dc.runtime_python(tmp_path, windows=True) == tmp_path / "python.exe"
    assert dc.runtime_python(tmp_path, windows=False) == tmp_path / "bin" / "python3"


def test_ollama_is_looked_for_where_its_installers_put_it(tmp_path):
    windows = dc.ollama_app_locations("win32", tmp_path, {"LOCALAPPDATA": str(tmp_path / "L")})
    assert windows == [tmp_path / "L" / "Programs" / "Ollama" / "ollama app.exe"]
    mac = dc.ollama_app_locations("darwin", tmp_path, {})
    assert mac == [Path("/Applications/Ollama.app"), tmp_path / "Applications" / "Ollama.app"]
    assert dc.find_ollama("linux", tmp_path, {}, which=lambda name: None) is None
    assert dc.find_ollama("linux", tmp_path, {}, which=lambda name: "/usr/bin/ollama") == Path("/usr/bin/ollama")


def test_start_prepare_only_reports_readiness(ollama, tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(dc, "load_settings", lambda env: settings_for(ollama))
    assert dc.main(["start", "--prepare-only"]) == 0
    assert "The AI model is ready" in capsys.readouterr().out
    ollama.models = []
    assert dc.main(["start", "--prepare-only"]) == 1


def test_an_app_that_cannot_start_its_service_falls_back_to_the_server(ollama, tmp_path):
    folder = tmp_path / "Ollama"
    folder.mkdir()
    (folder / "ollama app.exe").write_bytes(b"")
    (folder / "ollama.exe").write_bytes(b"")
    ollama.running = False
    server_json = tmp_path / "server.json"
    ops = FakeOps(ollama, server_json)
    started = []
    ops.find = lambda: folder / "ollama app.exe"

    def start(app):
        started.append(app.name)
        ollama.running = app.name == "ollama.exe"

    ops.start = start
    problems = dc.prepare(settings_for(ollama), False, True, ops=ops, platform="win32", server_json=server_json)
    assert problems == []
    assert started == ["ollama app.exe", "ollama.exe"]


def test_the_server_inside_each_kind_of_ollama_app(tmp_path):
    windows = tmp_path / "w"
    windows.mkdir()
    (windows / "ollama.exe").write_bytes(b"")
    assert dc.server_fallback(windows / "ollama app.exe") == windows / "ollama.exe"
    mac = tmp_path / "Ollama.app"
    (mac / "Contents" / "Resources").mkdir(parents=True)
    (mac / "Contents" / "Resources" / "ollama").write_bytes(b"")
    assert dc.server_fallback(mac) == mac / "Contents" / "Resources" / "ollama"
    assert dc.server_fallback(tmp_path / "ollama") is None
    assert dc.server_fallback(tmp_path / "missing" / "ollama app.exe") is None
