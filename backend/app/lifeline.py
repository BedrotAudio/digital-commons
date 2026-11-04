"""Exit with the process that started this gateway, on macOS and Linux.

dc.py passes the read end of a pipe and keeps the write end. The kernel closes that
end however dc.py stops, even on SIGKILL, so the read returns and the gateway shuts
down cleanly. macOS has no parent-death signal, and this needs none. Windows uses a
job object instead, so the variable is never set there.
"""

from __future__ import annotations

import os
import signal
import threading
from collections.abc import Callable, Mapping

LIFELINE_VARIABLE = "DC_LIFELINE_FD"


def _ask_to_exit() -> None:
    os.kill(os.getpid(), signal.SIGTERM)


def watch_lifeline(
    env: Mapping[str, str], on_lost: Callable[[], None] = _ask_to_exit
) -> threading.Thread | None:
    value = env.get(LIFELINE_VARIABLE)
    if not value:
        return None
    descriptor = int(value)

    def wait() -> None:
        try:
            while os.read(descriptor, 1):
                pass
        except OSError:
            pass
        on_lost()

    thread = threading.Thread(target=wait, name="lifeline", daemon=True)
    thread.start()
    return thread
