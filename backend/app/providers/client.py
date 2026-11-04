"""The one place upstream HTTP clients are made. See docs/security.md."""

from __future__ import annotations

import ssl
from collections.abc import AsyncIterator
from pathlib import Path

import httpx


async def aiter_lf_lines(response: httpx.Response) -> AsyncIterator[str]:
    """Lines ended by LF, with a trailing CR removed.

    httpx's aiter_lines splits like str.splitlines, so it also breaks on U+2028, U+2029,
    U+0085 and other separators that JSON leaves raw inside a string, which would cut
    one NDJSON chunk or SSE data line in two.
    """
    pending: list[str] = []
    async for text in response.aiter_text():
        *complete, rest = text.split("\n")
        for piece in complete:
            pending.append(piece)
            yield "".join(pending).removesuffix("\r")
            pending = []
        if rest:
            pending.append(rest)
    if pending:
        yield "".join(pending).removesuffix("\r")


def create_upstream_client(
    *,
    connect_timeout_s: float,
    ca_bundle: str | Path | None = None,
    transport: httpx.AsyncBaseTransport | None = None,
) -> httpx.AsyncClient:
    context = ssl.create_default_context(cafile=str(ca_bundle) if ca_bundle else None)
    return httpx.AsyncClient(
        trust_env=False,
        follow_redirects=False,
        verify=context,
        transport=transport,
        timeout=httpx.Timeout(connect=connect_timeout_s, read=None, write=30.0, pool=30.0),
    )
