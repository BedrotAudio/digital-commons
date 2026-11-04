"""Startup refusals: bind rule, tokens, data folder and config validation."""

from __future__ import annotations

import sys
from pathlib import Path, PurePosixPath, PureWindowsPath

import pytest

import app.__main__ as entry
from app.config.settings import ROOT, ConfigError, load_settings
from app.main import create_app
from app.security.identity import load_or_create_local_token
from app.security.locality import (
    BindRefused,
    compute_locality,
    enforce_loopback_bind,
    is_loopback_host,
    is_loopback_url,
)
from app.security.paths import check_data_dir, prepare_data_dir, resolve_data_dir, sync_service


def env_for(tmp_path: Path, **extra: str) -> dict[str, str]:
    return {"DC_DATA_DIR": str(tmp_path / "data"), **extra}


def permissions(**values: object) -> dict:
    return {"identity": {"user": {"permissions": values}}}


@pytest.mark.parametrize(
    "host",
    [
        "0.0.0.0",
        "127.1",
        "2130706433",
        "127.0.0.1.evil.gov",
        "localhost.evil.gov",
        "127.0.0.1@evil.gov",
        "192.168.1.5",
        "::",
        "",
    ],
)
def test_non_loopback_hosts_are_refused(host: str) -> None:
    assert not is_loopback_host(host)
    with pytest.raises(BindRefused):
        enforce_loopback_bind(host)


@pytest.mark.parametrize("host", ["127.0.0.1", "127.0.0.5", "localhost", "LOCALHOST", "::1"])
def test_loopback_hosts_pass(host: str) -> None:
    assert is_loopback_host(host)
    enforce_loopback_bind(host)


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1.evil.gov:11434",
        "http://localhost.evil.gov",
        "http://127.0.0.1@evil.gov",
        "http://evil.gov#@127.0.0.1",
        "http://0.0.0.0:11434",
        "http://127.1:11434",
        "http://2130706433",
        "http://user:pass@127.0.0.1:11434",
        "ftp://127.0.0.1",
        "http://[::1",
        "",
        None,
    ],
)
def test_bad_loopback_urls(url: str | None) -> None:
    assert not is_loopback_url(url)


def test_entry_point_refuses_command_line_host(monkeypatch, tmp_path, capsys) -> None:
    calls = []
    monkeypatch.setattr(entry.uvicorn, "run", lambda *a, **k: calls.append((a, k)))
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("DC_PROVIDER", "mock")
    assert entry.main(["--host", "0.0.0.0"]) == 2
    assert calls == []
    assert "Refusing to bind" in capsys.readouterr().err


def test_entry_point_refuses_configured_host(monkeypatch, tmp_path) -> None:
    calls = []
    monkeypatch.setattr(entry.uvicorn, "run", lambda *a, **k: calls.append((a, k)))
    config = tmp_path / "gateway.yaml"
    base = (ROOT / "config" / "development.yaml").read_text(encoding="utf-8")
    config.write_text(base.replace("host: 127.0.0.1 ", "host: 0.0.0.0 "), encoding="utf-8")
    monkeypatch.setenv("DC_CONFIG", str(config))
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("DC_PROVIDER", "mock")
    assert entry.main([]) == 2
    assert calls == []


def test_entry_point_uvicorn_flags(monkeypatch, tmp_path) -> None:
    calls = []
    monkeypatch.setattr(entry.uvicorn, "run", lambda *a, **k: calls.append(k))
    monkeypatch.setattr(entry, "configure_logging", lambda level: None)
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("DC_PROVIDER", "mock")
    assert entry.main(["--port", "8123"]) == 0
    assert calls[0]["proxy_headers"] is False
    assert calls[0]["access_log"] is False
    assert calls[0]["server_header"] is False
    assert calls[0]["host"] == "127.0.0.1"
    assert calls[0]["port"] == 8123


def test_direct_uvicorn_bind_is_refused_by_the_guard(make) -> None:
    """A non-loopback local address, as `uvicorn --host 0.0.0.0` would report it."""
    import asyncio

    gateway = make(provider="mock")
    messages: list[dict] = []

    async def send(message: dict) -> None:
        messages.append(message)

    async def receive() -> dict:
        return {"type": "http.request", "body": b"", "more_body": False}

    scope = {
        "type": "http",
        "method": "GET",
        "path": "/healthz",
        "raw_path": b"/healthz",
        "query_string": b"",
        "headers": [(b"host", b"127.0.0.1")],
        "server": ("192.168.1.5", 8000),
        "client": ("192.168.1.9", 5555),
        "http_version": "1.1",
        "scheme": "http",
    }
    asyncio.run(gateway.app(scope, receive, send))
    assert messages[0]["status"] == 403


@pytest.mark.parametrize("token", [None, "", "short", "x" * 31])
def test_shared_token_missing_or_short_is_refused(tmp_path, token) -> None:
    env = env_for(tmp_path)
    if token is not None:
        env["DC_GATEWAY_TOKEN"] = token
    with pytest.raises(ConfigError, match="DC_GATEWAY_TOKEN"):
        load_settings(env, overrides={"identity": {"mode": "shared_token"}})


def test_shared_token_of_32_characters_is_accepted(tmp_path) -> None:
    env = env_for(tmp_path, DC_GATEWAY_TOKEN="x" * 32)
    settings = load_settings(env, overrides={"identity": {"mode": "shared_token"}})
    assert settings.identity.mode == "shared_token"


@pytest.mark.parametrize(
    "path",
    [
        "C:/Users/a/OneDrive/DigitalCommons",
        "C:/Users/a/OneDrive - City of Example/data",
        "C:/Users/a/onedrive/data",
    ],
)
def test_onedrive_paths_are_refused(path) -> None:
    assert sync_service(PureWindowsPath(path), {}, "win32", "C:/Users/a") == "OneDrive"


def test_onedrive_in_a_folder_name_is_refused_on_any_platform(tmp_path) -> None:
    with pytest.raises(ConfigError, match="OneDrive"):
        load_settings({"DC_DATA_DIR": str(tmp_path / "OneDrive - City of Example" / "data")})


def test_folder_under_reported_sync_root_is_refused(tmp_path) -> None:
    root = tmp_path / "Corporate Sync"
    with pytest.raises(ConfigError, match="OneDrive"):
        load_settings({"DC_DATA_DIR": str(root / "data"), "OneDriveCommercial": str(root)})


def test_onedrive_refused_through_a_symlink(tmp_path) -> None:
    target = tmp_path / "OneDrive" / "data"
    target.mkdir(parents=True)
    link = tmp_path / "plain"
    try:
        link.symlink_to(target, target_is_directory=True)
    except OSError:
        pytest.skip("symlinks need extra rights on this machine")
    from app.security.paths import DataDirRefused

    with pytest.raises(DataDirRefused):
        check_data_dir(link, {})


def test_relative_data_dir_is_refused() -> None:
    with pytest.raises(ConfigError, match="absolute"):
        load_settings({"DC_DATA_DIR": "relative/folder"})


def test_data_dir_defaults_match_the_design() -> None:
    assert (
        str(resolve_data_dir({"LOCALAPPDATA": r"C:\L"}, "win32", r"C:\Users\A"))
        == r"C:\L\DigitalCommons"
    )
    assert (
        str(resolve_data_dir({}, "win32", r"C:\Users\A"))
        == r"C:\Users\A\AppData\Local\DigitalCommons"
    )
    assert (
        str(resolve_data_dir({"XDG_STATE_HOME": "/s"}, "linux", "/home/a"))
        == "/s/digital-commons"
    )
    assert (
        str(resolve_data_dir({"XDG_STATE_HOME": "rel"}, "linux", "/home/a"))
        == "/home/a/.local/state/digital-commons"
    )
    assert str(resolve_data_dir({"DC_DATA_DIR": "/d"}, "linux", "/home/a", "/c")) == "/d"
    mac = "/Users/a/Library/Application Support/DigitalCommons"
    assert str(resolve_data_dir({}, "darwin", "/Users/a")) == mac
    assert str(resolve_data_dir({"XDG_STATE_HOME": "/s"}, "darwin", "/Users/a")) == mac


@pytest.mark.parametrize(
    "path",
    [
        "/Users/a/Library/Mobile Documents/com~apple~CloudDocs/dc",
        "/Users/a/Library/CloudStorage/Dropbox/dc",
        "/Users/a/Library/CloudStorage/OneDrive-City/dc",
        "/Users/a/Desktop/dc",
        "/Users/a/Documents/dc",
        "/Users/A/DOCUMENTS/dc",
    ],
)
def test_folders_that_sync_off_a_mac_are_refused(path) -> None:
    assert sync_service(PurePosixPath(path), {}, "darwin", "/Users/a")


@pytest.mark.parametrize(
    "path",
    [
        "/Users/a/Library/Application Support/DigitalCommons",
        "/Users/a/dc",
        "/Users/b/Documents/dc",
        "/opt/Documents/dc",
    ],
)
def test_ordinary_mac_folders_are_allowed(path) -> None:
    assert sync_service(PurePosixPath(path), {}, "darwin", "/Users/a") is None


def test_mac_rules_do_not_apply_elsewhere() -> None:
    assert sync_service(PurePosixPath("/home/a/Documents/dc"), {}, "linux", "/home/a") is None


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX modes")
def test_data_dir_is_private_on_posix(tmp_path) -> None:
    path = tmp_path / "private"
    prepare_data_dir(path)
    assert path.stat().st_mode & 0o777 == 0o700


def test_local_token_is_256_bits_and_stable(tmp_path) -> None:
    first = load_or_create_local_token(tmp_path)
    assert len(first) == 64
    assert load_or_create_local_token(tmp_path) == first
    (tmp_path / "local-token").write_text("short")
    assert load_or_create_local_token(tmp_path) != first


@pytest.mark.parametrize("flag", ["manual_model_selection", "view_diagnostics"])
def test_advanced_permissions_require_advanced_mode(tmp_path, flag) -> None:
    with pytest.raises(ConfigError, match="advanced_mode"):
        load_settings(env_for(tmp_path), overrides=permissions(advanced_mode=False, **{flag: True}))


def test_unknown_allowed_tier_is_refused(tmp_path) -> None:
    with pytest.raises(ConfigError, match="unknown tiers"):
        load_settings(env_for(tmp_path), overrides=permissions(allowed_tiers=["root"]))


@pytest.mark.parametrize(
    "url",
    ["http://192.168.1.10:11434", "http://127.0.0.1.evil.gov:11434", "https://ai.example.gov"],
)
def test_ollama_must_be_loopback(tmp_path, url) -> None:
    with pytest.raises(ConfigError, match="loopback"):
        load_settings(env_for(tmp_path), overrides={"providers": {"ollama": {"base_url": url}}})


def org(url: str, allowed: list[str] | None = None) -> dict:
    return {
        "inference": {"default_provider": "org_server"},
        "providers": {"org_server": {"base_url": url}},
        "security": {"allowed_upstream_hosts": allowed or []},
    }


def test_inference_api_refuses_plain_http_to_a_remote_host(tmp_path) -> None:
    env = env_for(tmp_path, DC_ORG_SERVER_TOKEN="t")
    with pytest.raises(ConfigError, match="https"):
        load_settings(env, overrides=org("http://ai.example.gov", ["ai.example.gov"]))


def test_inference_api_refuses_an_unlisted_host(tmp_path) -> None:
    env = env_for(tmp_path, DC_ORG_SERVER_TOKEN="t")
    with pytest.raises(ConfigError, match="allowed_upstream_hosts"):
        load_settings(env, overrides=org("https://ai.example.gov"))


def test_inference_api_accepts_a_listed_https_host(tmp_path) -> None:
    env = env_for(tmp_path, DC_ORG_SERVER_TOKEN="t")
    settings = load_settings(env, overrides=org("https://AI.example.gov", ["ai.example.gov"]))
    assert settings.provider.kind == "inference_api"


def test_inference_api_allows_loopback_http(tmp_path) -> None:
    env = env_for(tmp_path, DC_ORG_SERVER_TOKEN="t")
    assert load_settings(env, overrides=org("http://127.0.0.1:9000")).provider_name == "org_server"


def test_inference_api_needs_its_token(tmp_path) -> None:
    with pytest.raises(ConfigError, match="DC_ORG_SERVER_TOKEN"):
        load_settings(env_for(tmp_path), overrides=org("http://127.0.0.1:9000"))


def test_inference_service_needs_its_token(tmp_path) -> None:
    with pytest.raises(ConfigError, match="DC_SERVICE_TOKEN"):
        load_settings(env_for(tmp_path), overrides={"inference_service": {"enabled": True}})


def test_shipped_config_loads_and_paths_ignore_the_working_directory(tmp_path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    settings = load_settings(env_for(tmp_path))
    assert settings.provider_name == "ollama"
    assert settings.documents_path == ROOT / "data" / "sample_documents"


def test_provider_override_and_unknown_provider(tmp_path) -> None:
    assert load_settings(env_for(tmp_path, DC_PROVIDER="mock")).provider.kind == "mock"
    with pytest.raises(ConfigError, match="Unknown provider"):
        load_settings(env_for(tmp_path, DC_PROVIDER="nothing"))


def test_unknown_config_key_is_refused(tmp_path) -> None:
    with pytest.raises(ConfigError, match="Invalid configuration"):
        load_settings(env_for(tmp_path), overrides={"gateway": {"hots": "x"}})


def test_shared_model_with_different_context_warns(tmp_path, caplog) -> None:
    catalog = {"catalog": {"ollama": {"fast": {"context": 4096}}}}
    with caplog.at_level("WARNING", logger="digital_commons.config"):
        load_settings(env_for(tmp_path), overrides={"models": catalog})
    assert "different context" in caplog.text


def test_locality_function() -> None:
    url = "http://127.0.0.1:11434"
    assert compute_locality("mock", None, False).kind == "demo"
    assert compute_locality("ollama", url, True).kind == "this_device"
    assert compute_locality("ollama", url, False).kind == "organization"
    assert compute_locality("inference_api", url, True).kind == "organization"


def test_create_app_refuses_nothing_silently_without_settings(monkeypatch, tmp_path) -> None:
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("DC_PROVIDER", "mock")
    assert create_app().title == "Digital Commons Gateway"
