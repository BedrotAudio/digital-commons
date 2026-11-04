"""The inference queue. One ticket is one place in line, then one running slot."""

from __future__ import annotations

import asyncio
from collections import deque

from app.models.errors import ApiError


class Ticket:
    __slots__ = ("waiter", "state")

    def __init__(self) -> None:
        self.waiter: asyncio.Future[None] | None = None
        self.state = "reserved"


class InferenceSlots:
    def __init__(self, concurrent: int, queued: int) -> None:
        self._concurrent = concurrent
        self._capacity = concurrent + queued
        self._outstanding = 0
        self._running = 0
        self._line: deque[Ticket] = deque()

    @property
    def running(self) -> int:
        return self._running

    @property
    def outstanding(self) -> int:
        return self._outstanding

    def reserve(self, *, internal: bool = False) -> Ticket:
        """Raise busy at capacity. Warm-up is internal and is never turned away."""
        if not internal and self._outstanding >= self._capacity:
            raise ApiError("busy", "the inference queue is full")
        self._outstanding += 1
        return Ticket()

    def can_start(self) -> bool:
        return self._running < self._concurrent and not self._line

    async def acquire(self, ticket: Ticket) -> None:
        if self.can_start():
            self._running += 1
            ticket.state = "running"
            return
        ticket.waiter = asyncio.get_running_loop().create_future()
        ticket.state = "waiting"
        self._line.append(ticket)
        try:
            await ticket.waiter
        except asyncio.CancelledError:
            if ticket.state == "waiting":
                self._line.remove(ticket)
            raise

    def release(self, ticket: Ticket) -> None:
        if ticket.state == "released":
            return
        if ticket.state == "waiting" and ticket in self._line:
            self._line.remove(ticket)
        was_running = ticket.state == "running"
        ticket.state = "released"
        self._outstanding -= 1
        if was_running:
            self._running -= 1
            self._wake_next()

    def _wake_next(self) -> None:
        while self._line and self._running < self._concurrent:
            ticket = self._line.popleft()
            if ticket.waiter is not None and not ticket.waiter.done():
                self._running += 1
                ticket.state = "running"
                ticket.waiter.set_result(None)
