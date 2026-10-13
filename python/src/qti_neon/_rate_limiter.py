"""Per-source token-bucket rate limiter used by the relay."""

from __future__ import annotations

import time
import threading

_VIOLATION_THRESHOLD = 10


class _RateLimiter:
    """Token-bucket rate limiter for a single source address.

    Tokens refill to ``max_tokens`` once per second. Packets that arrive when
    the bucket is empty are rejected and counted as violations. After more than
    `_VIOLATION_THRESHOLD` violations the source is considered throttled.
    """

    __slots__ = ("_lock", "_tokens", "_max_tokens", "_last_refill", "_violations", "_last_access")

    def __init__(self, max_tokens_per_second: int) -> None:
        self._lock = threading.Lock()
        self._max_tokens: int = max_tokens_per_second
        self._tokens: int = max_tokens_per_second
        self._last_refill: float = time.monotonic()
        self._violations: int = 0
        self._last_access: float = self._last_refill

    def allow_packet(self) -> bool:
        """Return ``True`` and consume a token if the packet is permitted."""
        with self._lock:
            now = time.monotonic()
            self._last_access = now
            self._refill(now)
            if self._tokens > 0:
                self._tokens -= 1
                return True
            self._violations += 1
            return False

    @property
    def is_throttled(self) -> bool:
        """``True`` when the source has exceeded the violation threshold."""
        return self._violations > _VIOLATION_THRESHOLD

    @property
    def last_access(self) -> float:
        """Monotonic timestamp (seconds) of the most recent ``allow_packet`` call."""
        with self._lock:
            return self._last_access

    def _refill(self, now: float) -> None:
        if now - self._last_refill >= 1.0:
            self._tokens = self._max_tokens
            self._last_refill = now
            if self._violations > 0:
                self._violations -= 1
