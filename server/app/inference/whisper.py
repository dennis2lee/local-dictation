"""faster-whisper backend: large-v3, CPU, INT8.

The model path is always a local directory. `local_files_only=True` is passed
explicitly so a typo in the path fails with "not found" instead of quietly
reaching for huggingface.co — which, on a closed network, would hang until the
first utterance timed out.
"""

from __future__ import annotations

import logging
import os
import threading
import time
from pathlib import Path

from app.inference.base import InferenceError, TranscriptionResult, Word
from app.settings import ModelSettings

log = logging.getLogger(__name__)

SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2
_WARMUP_SECONDS = 1.0


class FasterWhisperTranscriber:
    def __init__(self, settings: ModelSettings) -> None:
        try:
            import numpy as np
            from faster_whisper import WhisperModel
        except ImportError as exc:  # pragma: no cover - depends on host install
            raise InferenceError(
                "faster-whisper is not installed; install the 'inference' extra "
                "or run with --backend fake"
            ) from exc

        model_path = Path(settings.path)
        if not model_path.exists():
            raise InferenceError(f"model directory not found: {model_path}")
        if not model_path.is_dir():
            raise InferenceError(f"model path is not a directory: {model_path}")

        self._np = np
        self._settings = settings
        # CTranslate2 reads thread counts from the constructor, but a stray env
        # var can still oversubscribe the box. Pin it when the operator has.
        if settings.cpu_threads > 0:
            os.environ.setdefault("OMP_NUM_THREADS", str(settings.cpu_threads))

        log.info(
            "loading model",
            extra={
                "model_path": str(model_path),
                "device": settings.device,
                "compute_type": settings.compute_type,
                "language": settings.language,
                "cpu_threads": settings.cpu_threads,
            },
        )
        started = time.monotonic()
        self._model = WhisperModel(
            str(model_path),
            device=settings.device,
            compute_type=settings.compute_type,
            cpu_threads=settings.cpu_threads,
            num_workers=settings.num_workers,
            local_files_only=True,
        )
        log.info("model loaded", extra={"load_seconds": round(time.monotonic() - started, 2)})

        # CTranslate2 models are not safe to call concurrently from several
        # threads with num_workers=1; serialise here rather than hoping callers
        # do. The session limiter keeps the queue short.
        self._lock = threading.Lock()
        self._closed = False

    @property
    def name(self) -> str:
        return Path(self._settings.path).name

    @property
    def language(self) -> str:
        return self._settings.language

    def warmup(self) -> None:
        silence = b"\x00\x00" * int(SAMPLE_RATE * _WARMUP_SECONDS)
        started = time.monotonic()
        try:
            self.transcribe(silence)
        except InferenceError:
            log.warning("warmup decode failed; first utterance will be slower")
            return
        log.info("warmup complete", extra={"warmup_seconds": round(time.monotonic() - started, 2)})

    def transcribe(self, pcm: bytes, *, prompt: str = "") -> TranscriptionResult:
        if self._closed:
            raise InferenceError("transcriber is closed")
        if len(pcm) % BYTES_PER_SAMPLE:
            raise InferenceError("PCM payload is not a whole number of 16-bit samples")

        np = self._np
        samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
        audio_seconds = len(samples) / SAMPLE_RATE
        if audio_seconds == 0:
            return TranscriptionResult(text="")

        s = self._settings
        # The prompt is what the trimmed-away audio already said. Without it a
        # decode of the tail loses the sentence it belongs to and re-capitalises
        # mid-sentence.
        initial_prompt = " ".join(filter(None, (s.initial_prompt, prompt))) or None
        started = time.monotonic()
        try:
            with self._lock:
                segments, _info = self._model.transcribe(
                    samples,
                    language=s.language,
                    task="transcribe",
                    beam_size=s.beam_size,
                    temperature=s.temperature,
                    initial_prompt=initial_prompt,
                    condition_on_previous_text=s.condition_on_previous_text,
                    vad_filter=True,
                    vad_parameters={"min_silence_duration_ms": 500},
                    word_timestamps=True,
                )
                collected = list(segments)  # the generator is where decoding happens
        except Exception as exc:  # noqa: BLE001 - backend raises many concrete types
            raise InferenceError(f"decode failed: {exc}") from exc

        duration = time.monotonic() - started
        text = "".join(segment.text for segment in collected).strip()
        logprobs = [seg.avg_logprob for seg in collected if seg.avg_logprob is not None]

        words = tuple(
            Word(text=word.word, start=word.start, end=word.end)
            for segment in collected
            for word in (segment.words or ())
        )

        return TranscriptionResult(
            text=text,
            audio_seconds=audio_seconds,
            duration_seconds=duration,
            avg_logprob=sum(logprobs) / len(logprobs) if logprobs else None,
            segment_ends=tuple(seg.end for seg in collected),
            words=words,
        )

    def close(self) -> None:
        self._closed = True
        self._model = None
