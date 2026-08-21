"""Structured logging with a content guard.

`store_transcript: false` is the documented default, so it has to hold even when
someone later adds a helpful `log.debug("got %s", text)`. `TranscriptGuard`
raises in tests and drops the record in production rather than writing user
speech to the journal.
"""

from __future__ import annotations

import json
import logging
import sys
from typing import Any

#: Attributes the JSON formatter should not copy from LogRecord.
_STANDARD = frozenset(
    vars(logging.LogRecord("", 0, "", 0, "", (), None)).keys()
    | {"message", "asctime", "taskName"}
)

#: Keys a log record must never carry. Adding one here is cheaper than auditing
#: every call site.
FORBIDDEN_FIELDS = frozenset({"text", "transcript", "stable", "partial", "audio", "pcm", "hypothesis"})


class ContentLeak(AssertionError):
    """A log record tried to carry transcript or audio content."""


class TranscriptGuard(logging.Filter):
    def __init__(self, *, strict: bool = False) -> None:
        super().__init__()
        self.strict = strict

    def filter(self, record: logging.LogRecord) -> bool:
        leaked = FORBIDDEN_FIELDS.intersection(vars(record))
        if not leaked:
            return True
        if self.strict:
            raise ContentLeak(f"log record carries content field(s): {sorted(leaked)}")
        for field in leaked:
            setattr(record, field, "<redacted>")
        return True


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, Any] = {
            "ts": self.formatTime(record, "%Y-%m-%dT%H:%M:%S%z"),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        for key, value in vars(record).items():
            if key not in _STANDARD and not key.startswith("_"):
                payload[key] = value
        if record.exc_info:
            payload["exception"] = self.formatException(record.exc_info)
        return json.dumps(payload, ensure_ascii=False, default=str)


def configure_logging(level: str = "INFO", *, as_json: bool = True, strict: bool = False) -> None:
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(
        JsonFormatter()
        if as_json
        else logging.Formatter("%(asctime)s %(levelname)-7s %(name)s: %(message)s")
    )
    handler.addFilter(TranscriptGuard(strict=strict))

    root = logging.getLogger()
    for existing in list(root.handlers):
        root.removeHandler(existing)
    root.addHandler(handler)
    root.setLevel(level.upper())

    # uvicorn installs its own handlers; make them go through ours so the
    # content guard covers access logs too.
    for name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
        logger = logging.getLogger(name)
        logger.handlers.clear()
        logger.propagate = True
