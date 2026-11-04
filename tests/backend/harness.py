"""Test support: real uvicorn servers, a fake Ollama and a recording proxy."""

from __future__ import annotations

import asyncio
import json
import socket
import threading
import time
from collections import deque
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import httpx
import uvicorn
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, PlainTextResponse, RedirectResponse, Response
from starlette.responses import StreamingResponse
from starlette.routing import Route

from app.config.settings import Tuning, _merge, load_settings
from app.main import create_app

MODEL = "Gemma4:12b"
GATE_OK = {"cloud": {"disabled": True, "source": "config"}}
FAST_TUNING = Tuning(status_interval_s=0.05, warmup_poll_s=0.02, unfinished_turn_wait_s=0.5)


def wait_until(predicate: Callable[[], Any], timeout: float = 3.0, step: float = 0.01) -> Any:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(step)
    raise AssertionError(f"condition not met within {timeout}s")


class RunningServer:
    def __init__(self, app: Any, port: int = 0) -> None:
        config = uvicorn.Config(
            app,
            host="127.0.0.1",
            port=port,
            log_config=None,
            access_log=False,
            timeout_graceful_shutdown=2,
            lifespan="on",
        )
        self.server = uvicorn.Server(config)
        self.thread = threading.Thread(target=self.server.run, daemon=True)
        self.thread.start()
        wait_until(lambda: self.server.started, timeout=15)
        self.port = self.server.servers[0].sockets[0].getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"

    def stop(self) -> None:
        self.server.should_exit = True
        self.thread.join(timeout=15)


def chat_chunk(content: str = "", thinking: str | None = None) -> dict:
    message: dict[str, Any] = {"role": "assistant", "content": content}
    if thinking is not None:
        message["thinking"] = thinking
    return {"model": MODEL, "message": message, "done": False}


def final_chunk(reason: str = "stop", **extra: Any) -> dict:
    return {
        "model": MODEL,
        "message": {"role": "assistant", "content": ""},
        "done": True,
        "done_reason": reason,
        "total_duration": 2_000_000_000,
        "load_duration": 78_000_000,
        "prompt_eval_count": 23,
        "prompt_eval_cached_count": 18,
        "prompt_eval_duration": 500_000_000,
        "eval_count": 4,
        "eval_duration": 1_000_000_000,
        **extra,
    }


class FakeOllama:
    """Behaves like the facts in docs/implementation-notes.md, over a real socket."""

    def __init__(self) -> None:
        self.reset()
        self.app = Starlette(
            routes=[
                Route("/api/status", self._status),
                Route("/api/tags", self._tags),
                Route("/api/show", self._show, methods=["POST"]),
                Route("/api/ps", self._ps),
                Route("/api/version", self._version),
                Route("/api/chat", self._chat, methods=["POST"]),
                Route("/elsewhere", self._elsewhere, methods=["GET", "POST"]),
            ]
        )
        self.server = RunningServer(self.app)
        self.url = self.server.url

    def reset(self) -> None:
        self.status_code = 200
        self.status_body: Any = GATE_OK
        self.status_text: str | None = None
        self.status_script: deque[tuple[int, Any]] = deque()
        self.tags: list[dict] = [{"name": MODEL, "model": MODEL, "size": 6401126562}]
        self.show_body: dict = {"details": {"format": "gguf"}}
        self.ps: list[dict] = [self.loaded()]
        self.chunks: list[dict] = [chat_chunk("Hello"), chat_chunk(" there."), final_chunk()]
        # Leave non-ASCII raw, as Go's encoder does for U+0085. Controls stay escaped.
        self.raw_chunks = False
        self.chat_script: deque[tuple[int, dict]] = deque()
        self.first_token_delay = 0.0
        self.chunk_delay = 0.0
        self.end_without_final = False
        self.warm_delay = 0.0
        self.warm_status = 200
        self.warm_body: dict = {"done": True, "done_reason": "load"}
        self.redirect_paths: set[str] = set()
        self.requests: list[tuple[str, str, Any]] = []
        self.aborted = 0
        self.completed = 0
        self.elsewhere_hits = 0

    @staticmethod
    def loaded(context: int = 8192, name: str = MODEL) -> dict:
        return {
            "name": name,
            "model": name,
            "size": 6401126562,
            "size_vram": 0,
            "context_length": context,
            "expires_at": "2026-11-01T17:15:13.1234567-04:00",
        }

    def calls(self, path: str) -> list[Any]:
        return [body for _, seen, body in list(self.requests) if seen == path]

    @property
    def chats(self) -> list[dict]:
        return [body for body in self.calls("/api/chat") if body.get("messages")]

    @property
    def warmups(self) -> list[dict]:
        return [body for body in self.calls("/api/chat") if not body.get("messages")]

    def _log(self, request: Request, body: Any = None) -> Response | None:
        self.requests.append((request.method, request.url.path, body))
        if request.url.path in self.redirect_paths:
            return RedirectResponse(f"{self.url}/elsewhere", status_code=307)
        return None

    async def _elsewhere(self, request: Request) -> Response:
        self.elsewhere_hits += 1
        return JSONResponse({"models": [], "cloud": {"disabled": True}})

    async def _status(self, request: Request) -> Response:
        if early := self._log(request):
            return early
        if self.status_script:
            code, body = self.status_script.popleft()
            return JSONResponse(body, status_code=code)
        if self.status_text is not None:
            return PlainTextResponse(self.status_text, status_code=self.status_code)
        return JSONResponse(self.status_body, status_code=self.status_code)

    async def _tags(self, request: Request) -> Response:
        return self._log(request) or JSONResponse({"models": self.tags})

    async def _show(self, request: Request) -> Response:
        return self._log(request, await request.json()) or JSONResponse(self.show_body)

    async def _ps(self, request: Request) -> Response:
        return self._log(request) or JSONResponse({"models": self.ps})

    async def _version(self, request: Request) -> Response:
        return self._log(request) or JSONResponse({"version": "0.40.13"})

    async def _chat(self, request: Request) -> Response:
        body = await request.json()
        if early := self._log(request, body):
            return early
        if not body.get("messages"):
            await asyncio.sleep(self.warm_delay)
            return JSONResponse(self.warm_body, status_code=self.warm_status)
        if self.chat_script:
            code, reply = self.chat_script.popleft()
            if code != 200:
                return JSONResponse(reply, status_code=code)
        await asyncio.sleep(self.first_token_delay)
        return StreamingResponse(self._generate(), media_type="application/x-ndjson")

    async def _generate(self) -> Any:
        finished = False
        try:
            for chunk in self.chunks:
                yield json.dumps(chunk, ensure_ascii=not self.raw_chunks) + "\n"
                if self.chunk_delay:
                    await asyncio.sleep(self.chunk_delay)
            finished = True
        finally:
            if finished:
                self.completed += 1
            else:
                self.aborted += 1

    def stop(self) -> None:
        self.server.stop()


class RecordingProxy:
    """A TCP listener that counts every connection it receives."""

    def __init__(self) -> None:
        self.hits = 0
        self._socket = socket.socket()
        self._socket.bind(("127.0.0.1", 0))
        self._socket.listen(16)
        self.port = self._socket.getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self._thread = threading.Thread(target=self._serve, daemon=True)
        self._thread.start()

    def _serve(self) -> None:
        while True:
            try:
                connection, _ = self._socket.accept()
            except OSError:
                return
            self.hits += 1
            connection.recv(65536)
            connection.sendall(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
            connection.close()

    def stop(self) -> None:
        self._socket.close()


@dataclass
class ChatResult:
    status: int
    events: list[tuple[str, dict]]
    raw: str
    headers: httpx.Headers

    def data(self, event: str) -> dict:
        return next(body for name, body in self.events if name == event)

    def all(self, event: str) -> list[dict]:
        return [body for name, body in self.events if name == event]

    def names(self) -> list[str]:
        return [name for name, _ in self.events]

    @property
    def text(self) -> str:
        return "".join(body["text"] for name, body in self.events if name == "delta")

    @property
    def error(self) -> dict:
        return json.loads(self.raw)["error"]

    @property
    def conversation_id(self) -> str:
        return self.data("start")["conversation_id"]


def parse_sse(raw: str) -> list[tuple[str, dict]]:
    events = []
    for block in raw.split("\n\n"):
        lines = [line for line in block.split("\n") if line and not line.startswith(":")]
        if not lines:
            continue
        name = next(line[7:] for line in lines if line.startswith("event: "))
        data = next(line[6:] for line in lines if line.startswith("data: "))
        events.append((name, json.loads(data)))
    return events


class Gateway:
    """A real gateway on a real port, with a client that holds its token."""

    def __init__(self, settings: Any, env: dict[str, str], tuning: Tuning, token: str | None) -> None:
        self.settings = settings
        self.env = env
        self.app = create_app(settings, env=env, tuning=tuning)
        self.services = self.app.state.services
        self.server = RunningServer(self.app)
        self.url = self.server.url
        self.token = token or (settings.data_dir / "local-token").read_text().strip()
        self.client = httpx.Client(
            base_url=self.url,
            trust_env=False,
            timeout=30,
            headers={"Authorization": f"Bearer {self.token}"},
        )

    @property
    def data_dir(self) -> Path:
        return self.settings.data_dir

    def post_chat(self, **body: Any) -> ChatResult:
        body.setdefault("message", "hello")
        with self.client.stream("POST", "/v1/chat/stream", json=body) as response:
            raw = response.read().decode("utf-8")
        events = parse_sse(raw) if response.status_code == 200 else []
        return ChatResult(response.status_code, events, raw, response.headers)

    def conversations(self) -> list[dict]:
        return self.client.get("/v1/conversations").json()["conversations"]

    def conversation(self, conversation_id: str) -> dict:
        return self.client.get(f"/v1/conversations/{conversation_id}").json()

    def status(self) -> dict:
        return self.client.get("/v1/status").json()

    def stop(self) -> None:
        self.client.close()
        self.server.stop()


def build_gateway(
    data_dir: Path,
    fake_url: str | None,
    *,
    provider: str = "ollama",
    overrides: dict | None = None,
    env: dict[str, str] | None = None,
    tuning: Tuning = FAST_TUNING,
    token: str | None = None,
) -> Gateway:
    merged: dict = {
        "inference": {"warm_on_start": False, "default_provider": provider},
        "providers": {"ollama": {"base_url": fake_url or "http://127.0.0.1:1"}},
    }
    merged = _merge(merged, overrides or {})
    full_env = {"DC_DATA_DIR": str(data_dir), **(env or {})}
    return Gateway(load_settings(full_env, overrides=merged), full_env, tuning, token)
