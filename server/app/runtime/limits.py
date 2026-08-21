"""Concurrency admission control.

large-v3 on CPU has no graceful degradation: a third concurrent session does
not make everyone 50% slower, it makes everyone miss the latency budget while
the queue grows. So capacity is a hard gate — over the limit the server says
`server_busy` immediately and the client shows a recoverable error, rather than
accepting audio it cannot decode in time.
"""

from __future__ import annotations

import threading
from types import TracebackType


class SessionSlot:
    """A held capacity reservation. Releasing twice is a no-op."""

    def __init__(self, limiter: SessionLimiter) -> None:
        self._limiter = limiter
        self._released = False

    def release(self) -> None:
        if not self._released:
            self._released = True
            self._limiter._release()

    def __enter__(self) -> SessionSlot:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        self.release()


class SessionLimiter:
    def __init__(self, max_sessions: int) -> None:
        if max_sessions < 1:
            raise ValueError("max_sessions must be at least 1")
        self._max = max_sessions
        self._active = 0
        self._lock = threading.Lock()
        self.accepted_total = 0
        self.rejected_total = 0

    @property
    def max_sessions(self) -> int:
        return self._max

    @property
    def active(self) -> int:
        with self._lock:
            return self._active

    def try_acquire(self) -> SessionSlot | None:
        """Take a slot, or return None when the server is at capacity."""
        with self._lock:
            if self._active >= self._max:
                self.rejected_total += 1
                return None
            self._active += 1
            self.accepted_total += 1
        return SessionSlot(self)

    def _release(self) -> None:
        with self._lock:
            self._active = max(0, self._active - 1)
