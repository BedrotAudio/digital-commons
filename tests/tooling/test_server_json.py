"""The ~/.ollama/server.json merge. Every test uses a temporary path."""

from __future__ import annotations

import json

import pytest

import dc


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def read(path):
    return json.loads(path.read_text(encoding="utf-8"))


def test_creates_the_file_when_it_is_missing(tmp_path):
    path = tmp_path / ".ollama" / "server.json"
    assert dc.merge_server_json(path) is None
    assert read(path) == {"disable_ollama_cloud": True}
    assert list(path.parent.iterdir()) == [path]


def test_keeps_other_keys_and_their_order(tmp_path):
    path = tmp_path / "server.json"
    write(path, '{"host": "127.0.0.1:11434", "origins": ["a", "b"], "nested": {"x": 1}}')
    dc.merge_server_json(path)
    merged = read(path)
    assert merged == {
        "host": "127.0.0.1:11434",
        "origins": ["a", "b"],
        "nested": {"x": 1},
        "disable_ollama_cloud": True,
    }
    assert list(merged)[-1] == "disable_ollama_cloud"


def test_keeps_a_backup_of_the_original(tmp_path):
    path = tmp_path / "server.json"
    original = '{"host":"x"}'
    write(path, original)
    backup = dc.merge_server_json(path)
    assert backup == tmp_path / "server.json.bak"
    assert backup.read_text(encoding="utf-8") == original


def test_an_earlier_backup_is_never_overwritten(tmp_path):
    path = tmp_path / "server.json"
    write(path, '{"disable_ollama_cloud": false}')
    write(tmp_path / "server.json.bak", "precious")
    backup = dc.merge_server_json(path)
    assert backup == tmp_path / "server.json.bak.1"
    assert (tmp_path / "server.json.bak").read_text(encoding="utf-8") == "precious"
    assert read(path) == {"disable_ollama_cloud": True}


def test_does_nothing_when_already_true(tmp_path):
    path = tmp_path / "server.json"
    text = '{"disable_ollama_cloud": true,   "a": 1}'
    write(path, text)
    assert dc.merge_server_json(path) is None
    assert path.read_text(encoding="utf-8") == text
    assert not (tmp_path / "server.json.bak").exists()


@pytest.mark.parametrize("value", ["false", '"yes"', "1", "null"])
def test_a_value_that_is_not_true_is_replaced(tmp_path, value):
    path = tmp_path / "server.json"
    write(path, '{"disable_ollama_cloud": %s}' % value)
    dc.merge_server_json(path)
    assert read(path) == {"disable_ollama_cloud": True}


@pytest.mark.parametrize("text", ["{not json", "", "[1, 2]", '"text"', "42"])
def test_a_file_that_cannot_be_understood_is_left_alone(tmp_path, text):
    path = tmp_path / "server.json"
    write(path, text)
    with pytest.raises(dc.ToolError):
        dc.merge_server_json(path)
    assert path.read_text(encoding="utf-8") == text
    assert not (tmp_path / "server.json.bak").exists()


def test_a_byte_order_mark_is_tolerated(tmp_path):
    path = tmp_path / "server.json"
    path.write_bytes(b"\xef\xbb\xbf" + b'{"a": 1}')
    dc.merge_server_json(path)
    assert read(path) == {"a": 1, "disable_ollama_cloud": True}


def test_no_temporary_file_is_left_behind(tmp_path):
    path = tmp_path / "work" / "server.json"
    write(path, "{}")
    dc.merge_server_json(path)
    assert sorted(item.name for item in path.parent.iterdir()) == ["server.json", "server.json.bak"]


class Answers:
    def __init__(self, *replies):
        self.replies = list(replies)
        self.asked = 0

    def __call__(self, prompt):
        self.asked += 1
        if not self.replies:
            raise EOFError
        return self.replies.pop(0)


def test_the_explanation_shows_the_file_and_the_change(tmp_path, capsys):
    path = tmp_path / "server.json"
    write(path, '{"host": "x"}')
    dc.apply_cloud_step(path, assume_yes=False, interactive=False)
    shown = capsys.readouterr().out
    assert str(path) in shown
    assert '"host": "x"' in shown and '"disable_ollama_cloud": true' in shown
    assert "Ollama can pass a chat to a remote service" in shown


def test_without_a_person_to_ask_nothing_changes(tmp_path, capsys):
    path = tmp_path / "server.json"
    assert dc.apply_cloud_step(path, assume_yes=False, interactive=False) is False
    assert not path.exists()
    assert "--disable-ollama-cloud" in capsys.readouterr().out


def test_the_flag_answers_yes_in_advance(tmp_path, capsys):
    path = tmp_path / "server.json"
    ask = Answers()
    assert dc.apply_cloud_step(path, assume_yes=True, interactive=False, ask=ask) is True
    assert ask.asked == 0
    assert read(path) == {"disable_ollama_cloud": True}
    assert "must be restarted" in capsys.readouterr().out


@pytest.mark.parametrize("reply", ["y", "Y", "yes", " YES "])
def test_yes_changes_the_file(tmp_path, reply):
    path = tmp_path / "server.json"
    assert dc.apply_cloud_step(path, assume_yes=False, interactive=True, ask=Answers(reply)) is True
    assert read(path) == {"disable_ollama_cloud": True}


@pytest.mark.parametrize("reply", ["", "n", "no", "maybe", "ye"])
def test_anything_but_yes_changes_nothing(tmp_path, reply):
    path = tmp_path / "server.json"
    assert dc.apply_cloud_step(path, assume_yes=False, interactive=True, ask=Answers(reply)) is False
    assert not path.exists()


def test_end_of_input_counts_as_no(tmp_path):
    path = tmp_path / "server.json"
    assert dc.apply_cloud_step(path, assume_yes=False, interactive=True, ask=Answers()) is False
    assert not path.exists()


def test_an_already_correct_file_is_reported_not_rewritten(tmp_path, capsys):
    path = tmp_path / "server.json"
    write(path, '{"disable_ollama_cloud": true}')
    assert dc.apply_cloud_step(path, assume_yes=True, interactive=False) is False
    assert "already turns cloud access off" in capsys.readouterr().out
    assert not (tmp_path / "server.json.bak").exists()


def test_the_restart_note_names_the_right_steps():
    assert "system tray" in dc.ollama_restart_note("win32")
    assert "menu bar" in dc.ollama_restart_note("darwin")
    assert "systemctl restart ollama" in dc.ollama_restart_note("linux")
    assert "python3 dc.py doctor" in dc.ollama_restart_note("linux")


def test_the_real_home_is_not_used(isolated_home):
    assert dc.Path.home() == isolated_home
