"""The gateway leaves when dc.py is gone. dc.py's side is in tests/tooling/test_mac.py,
and test_gateway_lifecycle.py there kills a real dc.py."""

from __future__ import annotations

import os
import threading

from app.lifeline import LIFELINE_VARIABLE, watch_lifeline


def test_nothing_is_watched_without_the_variable() -> None:
    assert watch_lifeline({}) is None


def test_the_gateway_is_told_when_the_pipe_closes() -> None:
    reader, writer = os.pipe()
    lost = threading.Event()
    thread = watch_lifeline({LIFELINE_VARIABLE: str(reader)}, on_lost=lost.set)
    try:
        assert thread is not None and thread.daemon
        os.write(writer, b"x")
        assert not lost.wait(0.2), "only the end of the pipe counts, not data on it"
    finally:
        os.close(writer)
    assert lost.wait(5)
    thread.join(5)
    os.close(reader)


def test_a_descriptor_that_cannot_be_read_counts_as_gone() -> None:
    reader, writer = os.pipe()
    os.close(reader)
    os.close(writer)
    lost = threading.Event()
    watch_lifeline({LIFELINE_VARIABLE: str(reader)}, on_lost=lost.set)
    assert lost.wait(5)
