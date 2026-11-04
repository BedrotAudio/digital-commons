from __future__ import annotations

import sys
from collections.abc import Iterator
from pathlib import Path

import pytest

BACKEND = Path(__file__).resolve().parents[2] / "backend"
sys.path.insert(0, str(BACKEND))
sys.path.insert(0, str(Path(__file__).resolve().parent))

from harness import FakeOllama, Gateway, build_gateway  # noqa: E402


@pytest.fixture(scope="session")
def fake_server() -> Iterator[FakeOllama]:
    server = FakeOllama()
    yield server
    server.stop()


@pytest.fixture
def fake(fake_server: FakeOllama) -> FakeOllama:
    fake_server.reset()
    return fake_server


@pytest.fixture
def make(tmp_path: Path, fake: FakeOllama) -> Iterator:
    gateways: list[Gateway] = []
    counter = iter(range(100))

    def factory(**options) -> Gateway:
        data_dir = options.pop("data_dir", None) or tmp_path / f"data{next(counter)}"
        gateway = build_gateway(data_dir, options.pop("fake_url", fake.url), **options)
        gateways.append(gateway)
        return gateway

    factory.stop = lambda gateway: (gateways.remove(gateway), gateway.stop())
    yield factory
    for gateway in gateways:
        gateway.stop()
