#!/usr/bin/env python3
"""Check a running gateway end to end: its status, then one real streamed chat.

Standard library only, so it needs no virtual environment. The token comes from the
gateway's data folder, which is DC_DATA_DIR unless --data-dir says otherwise.

    python3 tests/ci/live_check.py
    python3 tests/ci/live_check.py --demo       for a gateway with DC_PROVIDER=mock
    python3 tests/ci/live_check.py --cloud-on   for an Ollama with cloud access on:
                                                the gateway must refuse to chat
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

QUESTION = "Say hello in three words."
READ_TIMEOUT_S = 300.0


class CheckFailed(Exception):
    pass


# The gateway refuses proxies and so does this check.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(url: str, token: str, body: dict | None = None) -> urllib.request.Request:
    headers = {"Authorization": f"Bearer {token}"}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode("utf-8")
    return urllib.request.Request(url, data=data, headers=headers)


def read_token(data_dir: str) -> str:
    path = Path(data_dir) / "local-token"
    try:
        return path.read_text(encoding="ascii").strip()
    except OSError as error:
        raise CheckFailed(f"The local token could not be read from {path}: {error}") from None


def wait_for_state(url: str, token: str, expected: str, limit_s: float, reason: str | None = None) -> dict:
    """Poll /v1/status. Warm-up on a slow computer takes minutes, so `starting` is normal.

    With `reason`, a passing state with another reason, such as `provider_unreachable`
    while Ollama is slow to answer the first poll, is waited out too. At the deadline
    the last matching state is returned, so the caller can name the wrong reason.
    """
    deadline = time.monotonic() + limit_s
    last = None
    view: dict = {}
    while True:
        try:
            with OPENER.open(request(f"{url}/v1/status", token), timeout=10) as response:
                view = json.load(response)
        except (OSError, ValueError) as error:
            view = {"state": f"no answer ({error})"}
        if view.get("state") != last:
            last = view.get("state")
            print(f"Status: {last}", flush=True)
        if last == expected and (reason is None or view.get("reason") == reason):
            return view
        if time.monotonic() >= deadline:
            if last == expected:
                return view
            raise CheckFailed(f"The status never became {expected!r}. The last reply was:\n{json.dumps(view, indent=2)}")
        time.sleep(1.0)


def read_events(response) -> list[tuple[str, dict]]:
    """Parse the stream as docs/api.md describes it: named events with one data line."""
    events: list[tuple[str, dict]] = []
    name = None
    data = None
    for raw in response:
        line = raw.decode("utf-8").rstrip("\n")
        if line.startswith(":"):
            continue
        if line.startswith("event: "):
            name = line[len("event: ") :]
        elif line.startswith("data: "):
            data = line[len("data: ") :]
        elif not line and name is not None and data is not None:
            events.append((name, json.loads(data)))
            if name == "delta":
                print(".", end="", flush=True)
            name = data = None
    print(flush=True)
    return events


def stream_one_chat(url: str, token: str) -> list[tuple[str, dict]]:
    body = {"message": QUESTION}
    try:
        with OPENER.open(request(f"{url}/v1/chat/stream", token, body), timeout=READ_TIMEOUT_S) as response:
            return read_events(response)
    except urllib.error.HTTPError as error:
        raise CheckFailed(f"The chat was refused: HTTP {error.code}\n{error.read().decode('utf-8', 'replace')}") from None
    except (OSError, ValueError) as error:
        raise CheckFailed(f"The chat failed: {error}") from None


def check_chat(events: list[tuple[str, dict]], locality: str) -> None:
    names = [name for name, _ in events]
    if not names or names[0] != "start":
        raise CheckFailed(f"The stream did not begin with start: {names[:5]}")
    route = events[0][1].get("route") or {}
    if route.get("locality") != locality:
        raise CheckFailed(f"The chat ran with locality {route.get('locality')!r}, not {locality!r}.")
    if "error" in names:
        raise CheckFailed(f"The stream reported an error: {dict(events)['error']}")
    deltas = [body.get("text", "") for name, body in events if name == "delta"]
    if not any(deltas):
        raise CheckFailed(f"No text arrived. The events were: {names}")
    if names[-1] != "done":
        raise CheckFailed(f"The stream ended without done. The events were: {names[-5:]}")
    print(f"Received {len(deltas)} deltas and a done event ({events[-1][1].get('finish_reason')}).")


def read_json(url: str, token: str | None = None) -> object:
    """GET a JSON reply. Ollama needs no token, the gateway does."""
    target = request(url, token) if token else urllib.request.Request(url)
    try:
        with OPENER.open(target, timeout=10) as response:
            return json.load(response)
    except (OSError, ValueError) as error:
        raise CheckFailed(f"{url} did not give a JSON reply: {error}") from None


def check_cloud_on(reply: object) -> None:
    """Ollama's own /api/status must say cloud access is on, or a refusal proves nothing."""
    cloud = reply.get("cloud") if isinstance(reply, dict) else None
    if not isinstance(cloud, dict) or cloud.get("disabled") is not False:
        raise CheckFailed(f"Ollama does not report cloud access on, so a refusal would prove nothing: {reply!r}")


def check_refused_status(view: dict) -> None:
    state, reason = view.get("state"), view.get("reason")
    if (state, reason) != ("unavailable", "cloud_access_enabled"):
        error = (view.get("inference") or {}).get("error")
        raise CheckFailed(
            f"The status is {state!r} with reason {reason!r}, not unavailable with "
            f"cloud_access_enabled. The gateway's error was: {error!r}"
        )
    kind = (view.get("locality") or {}).get("kind")
    if kind == "this_device":
        raise CheckFailed("The status claims this_device while cloud access is on.")
    disabled = (view.get("inference") or {}).get("cloud_access_disabled")
    if disabled is not False:
        raise CheckFailed(f"The status reports cloud_access_disabled {disabled!r}, not false.")


def send_refused_chat(url: str, token: str) -> tuple[int, dict]:
    """Send one chat and return the refusal. A chat that is accepted fails the check."""
    try:
        with OPENER.open(request(f"{url}/v1/chat/stream", token, {"message": QUESTION}), timeout=60) as response:
            names = [name for name, _ in read_events(response)]
    except urllib.error.HTTPError as error:
        try:
            return error.code, json.loads(error.read())
        except ValueError:
            return error.code, {}
    except OSError as error:
        raise CheckFailed(f"The chat failed without a reply: {error}") from None
    raise CheckFailed(f"The chat was accepted while cloud access is on. The events were: {names}")


def check_refusal(code: int, body: dict) -> None:
    error = body.get("error") if isinstance(body, dict) else None
    error_code = error.get("code") if isinstance(error, dict) else None
    if (code, error_code) != (503, "model_not_permitted"):
        raise CheckFailed(f"The chat was refused with HTTP {code} and code {error_code!r}, not 503 and model_not_permitted.")


def check_nothing_happened(conversations: object, loaded: object) -> None:
    """No conversation was kept, and Ollama has no model loaded, so nothing reached it."""
    kept = conversations.get("conversations") if isinstance(conversations, dict) else None
    if kept != []:
        raise CheckFailed(f"A refused chat left conversations behind: {kept!r}")
    models = loaded.get("models") if isinstance(loaded, dict) else None
    if models != []:
        raise CheckFailed(f"Ollama has a model loaded, so something reached it: {models!r}")


def run_cloud_on(args: argparse.Namespace, url: str, token: str) -> None:
    ollama = args.ollama_url.rstrip("/")
    check_cloud_on(read_json(f"{ollama}/api/status"))
    print("Ollama reports cloud access on.", flush=True)
    check_refused_status(wait_for_state(url, token, "unavailable", args.wait, reason="cloud_access_enabled"))
    print("Status is unavailable with reason cloud_access_enabled.", flush=True)
    check_refusal(*send_refused_chat(url, token))
    print("The chat was refused with 503 model_not_permitted.", flush=True)
    check_nothing_happened(read_json(f"{url}/v1/conversations", token), read_json(f"{ollama}/api/ps"))
    print("No conversation was kept and Ollama has no model loaded.")
    print("Live check passed.")


def parse_arguments(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Check a running gateway end to end.")
    parser.add_argument("--url", default="http://127.0.0.1:8000")
    parser.add_argument("--data-dir", default=os.environ.get("DC_DATA_DIR"))
    parser.add_argument("--wait", type=float, default=600.0, help="seconds to wait for the expected state")
    parser.add_argument("--ollama-url", default="http://127.0.0.1:11434", help="read by --cloud-on only")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument(
        "--demo",
        action="store_true",
        help="expect state demo and locality demo, for a gateway with DC_PROVIDER=mock",
    )
    modes.add_argument(
        "--cloud-on",
        action="store_true",
        help="expect Ollama to report cloud access on, and the gateway to refuse to chat",
    )
    return parser.parse_args(argv)


def run(args: argparse.Namespace) -> None:
    if not args.data_dir:
        raise CheckFailed("Set DC_DATA_DIR or pass --data-dir, so the token can be found.")
    url = args.url.rstrip("/")
    token = read_token(args.data_dir)
    if args.cloud_on:
        run_cloud_on(args, url, token)
        return
    state, locality = ("demo", "demo") if args.demo else ("ready", "this_device")
    view = wait_for_state(url, token, state, args.wait)
    kind = (view.get("locality") or {}).get("kind")
    if kind != locality:
        raise CheckFailed(f"The status reports locality {kind!r}, not {locality!r}.")
    inference = view.get("inference") or {}
    if not args.demo and inference.get("cloud_access_disabled") is not True:
        raise CheckFailed(f"Cloud access is not reported as off: {inference.get('cloud_access_disabled')!r}.")
    print(f"Status is {state} with locality {locality}.", flush=True)
    check_chat(stream_one_chat(url, token), locality)
    print("Live check passed.")


def main(argv: list[str] | None = None) -> int:
    try:
        run(parse_arguments(argv))
    except CheckFailed as error:
        print(f"Live check failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
