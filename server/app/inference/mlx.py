"""MLX backend: the same Whisper models, on an Apple Silicon GPU.

Why this exists alongside the faster-whisper one, rather than replacing it:
CTranslate2 has no Metal backend, and the wheels carry CUDA or nothing. On a
Mac that leaves the GPU sitting idle next to a decoder that is at the edge of
real time. Measured on a MacBook Air M5, same clip, same model, both with the
word timestamps the streaming layer needs — 4.59 s on the CPU against 0.71 s
here. Real-time factor 0.77 against 0.12.

This backend is macOS-only and its dependency is an optional extra, so nothing
about a Linux or Windows install changes by its existence. `--backend whisper`
remains the default everywhere.

The model is a different artifact from the CTranslate2 one: an MLX conversion,
`config.json` and `weights.safetensors` rather than `model.bin`. Point
`model.path` at a directory holding one — `fetch-model.sh large-v3-turbo-mlx`
downloads it — never at a repository id, for the same reason the faster-whisper
backend passes `local_files_only`: a typo must fail as "not found" rather than
quietly reaching for huggingface.co and hanging until the first utterance times
out.
"""

from __future__ import annotations

import logging
import threading
import time
from pathlib import Path

from app.inference.base import InferenceError, TranscriptionResult, Word
from app.settings import ModelSettings

log = logging.getLogger(__name__)

SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2
_WARMUP_SECONDS = 1.0

#: What an MLX conversion is made of. Checked before the first decode so a
#: CTranslate2 directory handed to this backend fails at startup with the
#: reason, rather than inside a decode pass. Two names because the older
#: conversions on mlx-community ship .npz and the newer ones .safetensors;
#: mlx_whisper reads either.
WEIGHTS = ("weights.safetensors", "weights.npz")


class MLXTranscriber:
    def __init__(self, settings: ModelSettings) -> None:
        try:
            import mlx.core as mx
            import mlx_whisper
            import numpy as np
        except ImportError as exc:  # pragma: no cover - depends on host install
            raise InferenceError(
                "mlx-whisper is not installed; install the 'mlx' extra on an "
                "Apple Silicon Mac, or run with --backend whisper"
            ) from exc

        model_path = Path(settings.path)
        if not model_path.exists():
            raise InferenceError(f"model directory not found: {model_path}")
        if not model_path.is_dir():
            raise InferenceError(f"model path is not a directory: {model_path}")
        if not any((model_path / name).is_file() for name in WEIGHTS):
            raise InferenceError(
                f"{model_path} holds none of {' or '.join(WEIGHTS)}. This "
                "backend wants an MLX conversion; a directory with model.bin "
                "in it is the CTranslate2 one, which --backend whisper reads."
            )

        self._np = np
        self._mlx_whisper = mlx_whisper
        self._settings = settings
        self._path = str(model_path)

        log.info(
            "loading model",
            extra={
                "model_path": self._path,
                "device": str(mx.default_device()),
                "language": settings.language,
            },
        )
        # Nothing loads here: mlx_whisper resolves and caches the model on its
        # first transcribe. warmup() is what actually pays for it, which is
        # what warmup is for — so a failure surfaces at startup either way.
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
            # Serialised for the same reason the CTranslate2 backend is: one
            # model, several sessions, and no promise of re-entrancy. The
            # session limiter keeps the queue short.
            with self._lock:
                raw = self._mlx_whisper.transcribe(
                    samples,
                    path_or_hf_repo=self._path,
                    language=s.language,
                    task="transcribe",
                    temperature=s.temperature,
                    initial_prompt=initial_prompt,
                    condition_on_previous_text=s.condition_on_previous_text,
                    word_timestamps=True,
                    verbose=None,
                )
        except Exception as exc:  # noqa: BLE001 - backend raises many concrete types
            raise InferenceError(f"decode failed: {exc}") from exc

        return _result(raw, audio_seconds=audio_seconds, duration=time.monotonic() - started)

    def close(self) -> None:
        self._closed = True
        self._mlx_whisper = None


def _result(raw: dict, *, audio_seconds: float, duration: float) -> TranscriptionResult:
    """Map one mlx_whisper result onto the contract every backend returns.

    Kept out of the class and free of any MLX import so it can be tested on a
    machine that cannot run the backend at all — which is every CI runner this
    project has.
    """
    segments = raw.get("segments") or []
    logprobs = [
        segment["avg_logprob"]
        for segment in segments
        if segment.get("avg_logprob") is not None
    ]
    words = tuple(
        Word(text=word["word"], start=float(word["start"]), end=float(word["end"]))
        for segment in segments
        for word in (segment.get("words") or ())
    )
    return TranscriptionResult(
        text=(raw.get("text") or "").strip(),
        audio_seconds=audio_seconds,
        duration_seconds=duration,
        avg_logprob=sum(logprobs) / len(logprobs) if logprobs else None,
        # float(): MLX hands back numpy scalars, and a numpy float in a frozen
        # dataclass compares fine but serialises to JSON as an error.
        segment_ends=tuple(float(segment["end"]) for segment in segments),
        words=words,
    )
