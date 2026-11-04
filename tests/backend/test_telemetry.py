"""Requirement 4: no telemetry. See the task notes on opentelemetry-api."""

from __future__ import annotations

import importlib.metadata
import os
import re
import subprocess
import sys
from pathlib import Path

import app
from app.config.settings import ROOT
from app.main import TELEMETRY_OFF, create_app

FORBIDDEN = re.compile(
    r"^(opentelemetry-(sdk|exporter|instrumentation|distro|proto)"
    r"|sentry|posthog|mixpanel|segment|amplitude|datadog|ddtrace|newrelic|logfire"
    r"|honeycomb|statsd|prometheus|bugsnag|rollbar|elastic-apm|applicationinsights"
    r"|azure-monitor|google-analytics|analytics)",
    re.IGNORECASE,
)


def test_no_sdk_exporter_or_analytics_package_is_installed() -> None:
    installed = sorted(
        {distribution.metadata["Name"] for distribution in importlib.metadata.distributions()}
    )
    assert installed, "metadata must be readable for this test to mean anything"
    assert [name for name in installed if FORBIDDEN.match(name)] == []


def test_opentelemetry_api_is_the_only_opentelemetry_package() -> None:
    names = {
        distribution.metadata["Name"].lower().replace("_", "-")
        for distribution in importlib.metadata.distributions()
    }
    assert {name for name in names if name.startswith("opentelemetry")} <= {"opentelemetry-api"}


def test_requirements_name_no_telemetry_package() -> None:
    backend = ROOT / "backend"
    for name in ("requirements.txt", "requirements-dev.txt"):
        for line in (backend / name).read_text(encoding="utf-8").splitlines():
            assert not FORBIDDEN.match(line), line


def test_runtime_pins_are_exact() -> None:
    lines = (ROOT / "backend" / "requirements.txt").read_text(encoding="utf-8").split()
    assert {line.split("==")[0].lower() for line in lines} == {
        "fastapi",
        "uvicorn",
        "httpx",
        "pydantic",
        "pyyaml",
    }
    assert all("==" in line for line in lines)


def assert_disabled(application) -> None:
    config = application._telemetry
    for key in ("tracing", "metrics", "logs", "operation_spans", "auto_configure"):
        assert config[key] is False, key
    assert application._native_telemetry.enabled() is False


def test_telemetry_is_off_with_nothing_set(monkeypatch, tmp_path) -> None:
    monkeypatch.delenv("FASTAPI_OTEL_AUTO_CONFIGURE", raising=False)
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("DC_PROVIDER", "mock")
    assert_disabled(create_app())


def test_telemetry_stays_off_with_auto_configure_true(monkeypatch, tmp_path) -> None:
    monkeypatch.setenv("FASTAPI_OTEL_AUTO_CONFIGURE", "true")
    monkeypatch.setenv("DC_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("DC_PROVIDER", "mock")
    assert_disabled(create_app())


def test_fastapi_would_honour_the_variable_without_our_setting(monkeypatch) -> None:
    """The control: without the constructor value, the variable really does switch it on."""
    from fastapi import FastAPI

    monkeypatch.setenv("FASTAPI_OTEL_AUTO_CONFIGURE", "true")
    assert FastAPI()._telemetry["auto_configure"] is True
    assert FastAPI(telemetry=TELEMETRY_OFF)._telemetry["auto_configure"] is False


def test_constructor_value_is_exactly_the_required_one() -> None:
    assert TELEMETRY_OFF == {
        "tracing": False,
        "metrics": False,
        "logs": False,
        "operation_spans": False,
        "auto_configure": False,
    }


def test_real_process_with_the_variable_set_loads_no_exporter(tmp_path) -> None:
    script = (
        "import sys\n"
        "from app.main import create_app\n"
        "create_app()\n"
        "bad = [name for name in sys.modules if name.startswith('opentelemetry.sdk')"
        " or 'exporter' in name]\n"
        "print('LOADED', bad)\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", script],
        cwd=ROOT / "backend",
        env={
            **os.environ,
            "FASTAPI_OTEL_AUTO_CONFIGURE": "true",
            "DC_DATA_DIR": str(tmp_path / "data"),
            "DC_PROVIDER": "mock",
        },
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, result.stderr
    assert "LOADED []" in result.stdout


def test_gateway_source_imports_no_telemetry() -> None:
    package = Path(app.__file__).parent
    for path in package.rglob("*.py"):
        assert not re.search(
            r"^\s*(import|from)\s+(opentelemetry|sentry|posthog)",
            path.read_text(encoding="utf-8"),
            re.MULTILINE,
        ), path.name
