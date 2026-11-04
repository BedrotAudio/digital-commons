"""Configuration reading, Ollama checks and the other pure helpers in dc.py."""

from __future__ import annotations

import json

import pytest

from conftest import free_port

import dc


def test_yaml_scalars_are_flattened_and_comments_removed():
    text = (
        "gateway:\n"
        "  host: 127.0.0.1   # loopback only\n"
        "  port: 8000\n"
        "storage:\n"
        "  data_dir:\n"
        "providers:\n"
        "  mock:\n"
        '    display_name: "Demo # one"\n'
        "suggestions:\n"
        "  - id: ignored\n"
    )
    assert dc.parse_simple_yaml(text) == {
        "gateway.host": "127.0.0.1",
        "gateway.port": "8000",
        "providers.mock.display_name": "Demo # one",
    }


def test_the_shipped_configuration_is_read():
    settings = dc.load_settings({})
    assert settings.port == 8000
    assert settings.host == "127.0.0.1"
    assert settings.provider == "ollama"
    assert settings.ollama_url == "http://127.0.0.1:11434"
    assert settings.identity_mode == "local"
    assert len(settings.models) >= 1


def test_the_provider_variable_overrides_the_file():
    assert dc.load_settings({"DC_PROVIDER": "mock"}).provider == "mock"


def test_a_local_file_beside_the_config_overrides_it(tmp_path):
    config = tmp_path / "dev.yaml"
    config.write_text("gateway:\n  port: 9000\n  host: 127.0.0.1\n", encoding="utf-8")
    (tmp_path / "dev.local.yaml").write_text("gateway:\n  port: 9100\n", encoding="utf-8")
    settings = dc.load_settings({"DC_CONFIG": str(config)})
    assert settings.port == 9100


def test_a_bad_port_is_explained(tmp_path):
    config = tmp_path / "dev.yaml"
    config.write_text("gateway:\n  port: eight\n", encoding="utf-8")
    with pytest.raises(dc.ToolError):
        dc.load_settings({"DC_CONFIG": str(config)})


def test_only_enabled_ollama_models_are_listed():
    models = dc.parse_simple_yaml(
        "tiers:\n  fast:\n    label: F\n  advanced:\n    enabled: false\n"
        "catalog:\n  ollama:\n    fast:\n      name: small:1b\n    standard:\n      name: small:1b\n"
        "    advanced:\n      name: big:70b\n  mock:\n    fast:\n      name: demo-fast\n"
    )
    assert dc.configured_ollama_models(models) == ["small:1b"]


@pytest.mark.parametrize(
    "body,expected_state",
    [
        ({"cloud": {"disabled": True, "source": "config"}}, "disabled"),
        ({"cloud": {"disabled": False, "source": "none"}}, "not_disabled"),
        ({"cloud": {"disabled": "true"}}, "not_disabled"),
        ({"cloud": {"disabled": 1}}, "not_disabled"),
        ({"cloud": {}}, "not_disabled"),
        ({}, "not_disabled"),
    ],
)
def test_cloud_status_needs_an_exact_true(body, expected_state):
    state, _ = dc.interpret_cloud_status(200, json.dumps(body).encode())
    assert state == expected_state


def test_cloud_status_that_cannot_be_read_is_not_treated_as_off():
    assert dc.interpret_cloud_status(None, b"")[0] == "unreachable"
    assert dc.interpret_cloud_status(404, b"{}")[0] == "unknown"
    assert dc.interpret_cloud_status(200, b"not json")[0] == "unknown"
    assert dc.interpret_cloud_status(200, b"[1]")[0] == "unknown"


def test_the_cloud_source_is_reported():
    assert dc.interpret_cloud_status(200, b'{"cloud":{"disabled":true,"source":"env"}}') == ("disabled", "env")


def test_model_names_compare_the_way_ollama_does():
    assert dc.normalize_model_name("Gemma4:12b") == "gemma4:12b"
    assert dc.normalize_model_name("llama") == "llama:latest"
    assert dc.normalize_model_name("registry.local:5000/team/model") == "registry.local:5000/team/model:latest"


def test_installed_models_are_matched_without_regard_to_case():
    tags = {"models": [{"name": "gemma4:12B"}]}
    assert dc.compare_models(["Gemma4:12b"], tags) == ([], [])


def test_missing_and_remote_models_are_separated():
    tags = {
        "models": [
            {"name": "present:1b"},
            {"name": "stub:cloud", "remote_model": "x", "remote_host": "https://ollama.com:443"},
        ]
    }
    missing, remote = dc.compare_models(["present:1b", "absent:2b", "stub:cloud"], tags)
    assert missing == ["absent:2b"]
    assert remote == ["stub:cloud"]


@pytest.mark.parametrize("tags", [None, [], {}, {"models": None}, {"models": "x"}, {"models": [1, None]}])
def test_odd_tag_replies_mean_nothing_is_installed(tags):
    assert dc.compare_models(["m:1"], tags) == (["m:1"], [])


@pytest.mark.parametrize(
    "status,body,healthy",
    [
        (200, b'{"status":"ok"}', True),
        (200, b'{"status": "ok"}', True),
        (200, b'{"status":"starting"}', False),
        (200, b"ok", False),
        (500, b'{"status":"ok"}', False),
        (None, b"", False),
    ],
)
def test_health_reply(status, body, healthy):
    assert dc.is_healthy_reply(status, body) is healthy


def test_http_get_reports_no_answer_as_none():
    port = free_port()
    assert dc.http_get(f"http://127.0.0.1:{port}/healthz", timeout=1.0) == (None, b"")


def test_commands_are_written_for_the_platform():
    assert dc.run_hint("dev", "win32") == "python dc.py dev"
    assert dc.run_hint("dev", "darwin") == "python3 dc.py dev"
    assert dc.run_hint("dev", "linux") == "python3 dc.py dev"
    assert dc.run_hint("", "linux") == "python3 dc.py"


def test_venv_python_path():
    root = dc.Path("/x/.venv")
    assert dc.venv_python(root, windows=True) == root / "Scripts" / "python.exe"
    assert dc.venv_python(root, windows=False) == root / "bin" / "python"


def test_port_check_sees_a_bound_port():
    import socket

    port = free_port()
    assert dc.port_is_free("127.0.0.1", port)
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as holder:
        holder.bind(("127.0.0.1", port))
        holder.listen()
        assert not dc.port_is_free("127.0.0.1", port)


def make_tree(root):
    for name in (
        "bin",
        "dist",
        ".tools/go/bin",
        ".git/objects",
        "backend/.venv/Lib/__pycache__",
        "backend/app/__pycache__",
        "tests/backend/.pytest_cache",
        "terminal/internal",
        "data/sample_documents",
    ):
        (root / name).mkdir(parents=True)


def relative(root, paths):
    return sorted(path.relative_to(root).as_posix() for path in paths)


def test_clean_removes_output_and_caches_only(tmp_path):
    root = tmp_path / "repo"
    make_tree(root)
    targets = dc.clean_targets(root, tmp_path / "data", remove_venv=False)
    assert relative(root, targets) == [
        "backend/app/__pycache__",
        "bin",
        "dist",
        "tests/backend/.pytest_cache",
    ]


def test_clean_all_adds_the_venv_and_still_keeps_tools_and_git(tmp_path):
    root = tmp_path / "repo"
    make_tree(root)
    targets = relative(root, dc.clean_targets(root, tmp_path / "data", remove_venv=True))
    assert "backend/.venv" in targets
    assert not any(item.startswith((".tools", ".git")) for item in targets)


def test_clean_never_removes_a_folder_holding_the_data(tmp_path):
    root = tmp_path / "repo"
    make_tree(root)
    data = root / "dist" / "userdata"
    data.mkdir()
    targets = dc.clean_targets(root, data, remove_venv=False)
    assert root / "dist" not in targets
    assert root / "bin" in targets


def test_the_pid_record_round_trips(tmp_path):
    record = dc.GatewayRecord(pid=4242, started="stamp", mode="dev", log="x.log")
    dc.write_record(tmp_path, record)
    assert dc.read_record(tmp_path) == record
    dc.clear_record(tmp_path)
    assert dc.read_record(tmp_path) is None
    dc.clear_record(tmp_path)


@pytest.mark.parametrize("text", ["", "{", "[]", '{"pid": "x"}', '{"pid": 1}'])
def test_a_damaged_pid_file_is_ignored(tmp_path, text):
    dc.pid_file(tmp_path).write_text(text, encoding="utf-8")
    assert dc.read_record(tmp_path) is None
    assert dc.running_record(tmp_path) is None


def test_a_pid_with_a_different_start_time_is_not_our_gateway(tmp_path):
    import os

    dc.write_record(tmp_path, dc.GatewayRecord(os.getpid(), "not the real start time", "dev"))
    assert dc.running_record(tmp_path) is None
    dc.write_record(tmp_path, dc.GatewayRecord(os.getpid(), dc.process_start_time(os.getpid()), "dev"))
    assert dc.running_record(tmp_path) is not None


def test_a_process_that_does_not_exist_has_no_start_time():
    assert dc.process_start_time(0) is None
    assert dc.process_start_time(2**22 + 12345) is None


def test_the_pid_file_is_not_world_readable(tmp_path):
    import os
    import stat

    dc.write_record(tmp_path, dc.GatewayRecord(1, "x", "dev"))
    if os.name != "nt":
        assert stat.S_IMODE(dc.pid_file(tmp_path).stat().st_mode) == 0o600
