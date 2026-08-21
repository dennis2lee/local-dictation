"""Backend selection."""

from __future__ import annotations

import logging

from app.inference.base import Transcriber
from app.settings import ModelSettings

log = logging.getLogger(__name__)


def create_transcriber(settings: ModelSettings, *, backend: str = "whisper") -> Transcriber:
    """Build the configured backend.

    `backend="fake"` exists so that the API, streaming and packaging layers can
    be exercised on a machine with no model on disk. It is never selected by a
    config file — only by an explicit `--backend fake` flag.
    """
    if backend == "fake":
        from app.inference.fake import FakeTranscriber

        log.warning("using the fake transcriber; output is scripted, not recognised speech")
        return FakeTranscriber(language=settings.language)

    if backend == "whisper":
        from app.inference.whisper import FasterWhisperTranscriber

        return FasterWhisperTranscriber(settings)

    raise ValueError(f"unknown inference backend: {backend!r}")
