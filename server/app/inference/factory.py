"""Backend selection."""

from __future__ import annotations

import logging

from dataclasses import replace

from app.inference.base import Transcriber
from app.settings import ModelSettings

log = logging.getLogger(__name__)


def create_draft_transcriber(
    settings: ModelSettings, *, backend: str = "whisper"
) -> Transcriber | None:
    """Build the optional low-latency model used for partial text.

    It inherits the primary model's backend, device, quantisation and thread
    settings: running the draft on different hardware than the model it is
    racing would make the latency numbers meaningless, and a draft on one
    backend with the accurate model on another is two model formats to keep
    straight for no benefit.
    """
    if not settings.draft_path:
        return None

    draft_settings = replace(settings, path=settings.draft_path, draft_path=None)
    log.info("loading the draft model", extra={"draft_path": settings.draft_path})
    return create_transcriber(draft_settings, backend=backend)


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

    if backend == "mlx":
        from app.inference.mlx import MLXTranscriber

        return MLXTranscriber(settings)

    raise ValueError(f"unknown inference backend: {backend!r}")
