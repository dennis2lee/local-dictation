"""Everything a request handler needs, built once at startup."""

from __future__ import annotations

import logging
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field

from app.inference.base import Transcriber
from app.observability.metrics import Metrics
from app.runtime.limits import SessionLimiter
from app.settings import Settings

log = logging.getLogger(__name__)


@dataclass
class AppState:
    settings: Settings
    transcriber: Transcriber
    limiter: SessionLimiter
    metrics: Metrics
    executor: ThreadPoolExecutor
    #: Flips true once the warmup decode has run. `/health/ready` gates on it so
    #: a rolling restart does not send traffic to a process still loading 3 GB
    #: of weights.
    ready: bool = field(default=False)
    shutting_down: bool = field(default=False)

    @classmethod
    def create(cls, settings: Settings, transcriber: Transcriber) -> AppState:
        return cls(
            settings=settings,
            transcriber=transcriber,
            limiter=SessionLimiter(settings.limits.max_sessions),
            metrics=Metrics(language=settings.language, model=transcriber.name),
            # One worker per admissible session: the model serialises internally
            # anyway, but a dedicated thread keeps a slow decode from blocking
            # another session's flush.
            executor=ThreadPoolExecutor(
                max_workers=settings.limits.max_sessions,
                thread_name_prefix="decode",
            ),
        )

    def warmup(self) -> None:
        try:
            self.transcriber.warmup()
        except Exception:  # noqa: BLE001 - readiness must not depend on it
            log.exception("warmup failed; serving anyway with a cold model")
        self.ready = True

    def shutdown(self) -> None:
        self.shutting_down = True
        self.ready = False
        self.executor.shutdown(wait=True, cancel_futures=True)
        self.transcriber.close()
