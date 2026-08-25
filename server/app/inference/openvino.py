"""OpenVINO backend: the same Whisper models, on an Intel GPU.

Why this exists alongside the faster-whisper one, rather than replacing it:
CTranslate2 has no Intel GPU backend, so on a machine whose fastest processor
is an Arc iGPU the accurate model decodes on the CPU while the GPU idles. This
is the same argument that produced the MLX backend for Apple Silicon, and it
gets the same shape — one file, an optional dependency, and no change to what
any other platform installs. `--backend whisper` remains the default
everywhere.

The first supported target is Intel Arc 140V (Lunar Lake) on Windows, but
nothing here is specific to it: `model.device` is an OpenVINO device string, so
the same backend serves an Arc A-series card, an older Iris Xe, or the NPU.

**No silent fallback to the CPU.** OpenVINO will happily run a GPU-targeted
model on the CPU, and a user who chose the GPU and got the CPU sees only that
dictation is slow — never why. So a device that is not present is a startup
error naming the devices that are, and the resolved device name is reported in
`/health/ready` so the choice can be confirmed from outside the process.

The model is a third artifact, distinct from the other two backends':
`openvino_encoder_model.xml` and friends rather than `model.bin` (CTranslate2)
or `weights.safetensors` (MLX). `fetch-model.sh large-v3-turbo-openvino-int8`
downloads one. Point `model.path` at a directory holding it, never at a
repository id, for the same reason the other backends refuse to: a typo must
fail as "not found" rather than quietly reaching for huggingface.co.
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

#: What an OpenVINO IR conversion of Whisper is made of. The encoder is the one
#: file every export produces under the same name, so it is what preflight and
#: the constructor look for.
WEIGHTS = ("openvino_encoder_model.xml",)

#: Devices that mean "let OpenVINO decide". Refused rather than accepted: the
#: whole point of choosing the GPU is knowing you got it, and AUTO silently
#: lands on the CPU when the GPU plugin fails to load. Someone who genuinely
#: wants either can name the one they want.
_UNDECIDED = ("AUTO", "HETERO", "MULTI", "BATCH")

#: Friendly spellings accepted in `model.device`, mapped onto OpenVINO's own.
_ALIASES = {
    "CUDA": "GPU",
    "INTEL-GPU": "GPU",
    "INTEL_GPU": "GPU",
    "IGPU": "GPU",
    "ARC": "GPU",
}


def normalise_device(device: str) -> str:
    """Map `model.device` onto an OpenVINO device string.

    OpenVINO device names are upper case and carry a suffix two ways: an index
    (`GPU.1` picks the second Intel GPU on a machine with a discrete card
    beside the integrated one) and a device list (`MULTI:GPU,CPU`). Both are
    preserved; only the family name in front is translated. Everything else is
    passed through untouched so a device this code has never heard of still
    reaches the runtime.
    """
    cleaned = (device or "").strip().upper()
    if not cleaned:
        return "CPU"
    cut = min((at for at in (cleaned.find("."), cleaned.find(":")) if at >= 0), default=-1)
    if cut < 0:
        return _ALIASES.get(cleaned, cleaned)
    head, suffix = cleaned[:cut], cleaned[cut:]
    return _ALIASES.get(head, head) + suffix


def device_family(device: str) -> str:
    """The device name with any index or device list stripped off.

    `GPU.1` and `MULTI:GPU,CPU` are both answered by their first word, which is
    what decides whether a device is one this backend will accept and which
    plugin has to be present for it.
    """
    for separator in (".", ":"):
        device = device.partition(separator)[0]
    return device


class OpenVINOTranscriber:
    def __init__(self, settings: ModelSettings) -> None:
        try:
            import numpy as np
            import openvino
            import openvino_genai
        except ImportError as exc:  # pragma: no cover - depends on host install
            raise InferenceError(
                "openvino-genai is not installed; install the 'openvino' extra, "
                "or run with --backend whisper"
            ) from exc

        model_path = Path(settings.path)
        if not model_path.exists():
            raise InferenceError(f"model directory not found: {model_path}")
        if not model_path.is_dir():
            raise InferenceError(f"model path is not a directory: {model_path}")
        if not any((model_path / name).is_file() for name in WEIGHTS):
            raise InferenceError(
                f"{model_path} holds no {WEIGHTS[0]}. This backend wants an "
                "OpenVINO IR export; a directory with model.bin in it is the "
                "CTranslate2 one, which --backend whisper reads, and one with "
                "weights.safetensors is the MLX one."
            )

        device = normalise_device(settings.device)
        if device_family(device) in _UNDECIDED:
            raise InferenceError(
                f"model.device is {settings.device!r}, which lets OpenVINO choose "
                "the device at run time. Name the one you want — 'GPU' or 'CPU' — "
                "so that a GPU that fails to load is an error rather than a "
                "silent fall back to the CPU."
            )
        _require_device(openvino, device)

        self._np = np
        self._settings = settings
        self._path = str(model_path)
        self._device = device
        self._device_name = _device_name(openvino, device)

        # Compiling a Whisper graph for the GPU takes tens of seconds, and it
        # produces the same blob every time. Caching it beside the model turns
        # every start after the first into a load.
        cache_dir = model_path / ".openvino-cache"
        properties = {
            "CACHE_DIR": str(cache_dir),
            # Dictation decodes one short utterance at a time; throughput hints
            # would trade the latency this whole project is built around.
            "PERFORMANCE_HINT": "LATENCY",
        }

        log.info(
            "loading model",
            extra={
                "model_path": self._path,
                "device": device,
                "device_name": self._device_name,
                "language": settings.language,
            },
        )
        started = time.monotonic()
        try:
            # word_timestamps has to be set here, not per-decode: it makes the
            # pipeline decompose the decoder's cross-attention layers, which is
            # a property of the compiled graph. Asking for word timings at
            # generate() time on a pipeline built without it returns nothing.
            self._pipeline = _build_pipeline(
                openvino_genai, self._path, device, properties
            )
        except Exception as exc:  # noqa: BLE001 - runtime raises many concrete types
            raise InferenceError(
                f"could not load {self._path} on {device}: {exc}"
            ) from exc
        log.info(
            "model compiled",
            extra={
                "device": device,
                "compile_seconds": round(time.monotonic() - started, 2),
                "cache_dir": str(cache_dir),
            },
        )

        self._lock = threading.Lock()
        self._closed = False
        self._warned_about_words = False

    @property
    def name(self) -> str:
        return Path(self._settings.path).name

    @property
    def language(self) -> str:
        return self._settings.language

    @property
    def device(self) -> str:
        """The OpenVINO device this instance actually compiled for."""
        return self._device

    @property
    def device_name(self) -> str:
        """The full product name, e.g. 'Intel(R) Arc(TM) 140V GPU'."""
        return self._device_name

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
        initial_prompt = " ".join(filter(None, (s.initial_prompt, prompt))) or None
        started = time.monotonic()
        try:
            # Serialised for the same reason the other backends are: one
            # compiled model, several sessions, and no promise of re-entrancy.
            # On a GPU it matters more, not less — two decodes interleaving on
            # one device queue is slower than either alone.
            with self._lock:
                raw = self._pipeline.generate(
                    samples,
                    language=f"<|{s.language}|>",
                    task="transcribe",
                    return_timestamps=True,
                    word_timestamps=True,
                    **({"initial_prompt": initial_prompt} if initial_prompt else {}),
                )
        except Exception as exc:  # noqa: BLE001 - runtime raises many concrete types
            raise InferenceError(f"decode failed: {exc}") from exc

        result = _result(raw, audio_seconds=audio_seconds, duration=time.monotonic() - started)
        if result.text and not result.words and not self._warned_about_words:
            # Said once, not per decode. Without word timings the streaming
            # layer cannot trim audio it has already committed, so the cost of
            # a decode pass grows with the length of the utterance instead of
            # staying flat. See app/streaming/session.py.
            self._warned_about_words = True
            log.warning(
                "this OpenVINO build returned no word timings; long utterances "
                "will decode more slowly as they grow",
                extra={"model_path": self._path},
            )
        return result

    def close(self) -> None:
        self._closed = True
        self._pipeline = None


def _build_pipeline(genai, model_path: str, device: str, properties: dict):
    """Construct the ASR pipeline, across the two names it has had.

    `WhisperPipeline` was renamed `ASRPipeline`; both spellings are in the wild
    depending on which openvino-genai a machine has. Trying the current name
    first and falling back keeps one file working against both rather than
    pinning a version this project does not otherwise care about.
    """
    factory = getattr(genai, "ASRPipeline", None) or getattr(genai, "WhisperPipeline", None)
    if factory is None:
        raise InferenceError(
            "openvino_genai exposes neither ASRPipeline nor WhisperPipeline; "
            "the installed version is too old for this backend"
        )
    return factory(model_path, device, word_timestamps=True, **properties)


def _require_device(openvino, device: str) -> None:
    """Refuse to start on a device this machine does not have.

    OpenVINO does not fail here by itself — it raises somewhere inside the
    compile with a message about a plugin, which reads as a broken install
    rather than as "there is no Intel GPU in this computer".
    """
    try:
        available = list(openvino.Core().available_devices)
    except Exception as exc:  # noqa: BLE001 - a broken install raises from the core
        raise InferenceError(f"could not enumerate OpenVINO devices: {exc}") from exc

    # GPU matches GPU.0: OpenVINO reports indexed names on a machine with more
    # than one, and the bare name addresses the first.
    head = device_family(device)
    if device in available or (device == head and any(
        name == head or name.startswith(head + ".") for name in available
    )):
        return

    raise InferenceError(
        f"OpenVINO has no device {device!r} on this machine. It offers: "
        f"{', '.join(available) or 'nothing at all'}. "
        + (
            "An Intel GPU needs a current Intel graphics driver and the "
            "OpenVINO GPU plugin; without both, only CPU is listed."
            if head == "GPU"
            else "Set model.device to one of the above."
        )
    )


def _device_name(openvino, device: str) -> str:
    """The device's product name, or the device string if it will not say."""
    try:
        return str(openvino.Core().get_property(device, "FULL_DEVICE_NAME"))
    except Exception:  # noqa: BLE001 - a plugin that will not answer is not fatal
        return device


def _sequence(value) -> list:
    """Take the first sequence out of a per-sequence result.

    openvino-genai groups both chunks and words by generated sequence, so
    `words[0]` is the first sequence's list of words rather than the first
    word — and reading it as a word raises AttributeError on the first real
    decode. Dictation asks for one sequence, so the first is the only one.

    The documented shape is the flat one, and some builds return it, so both
    are accepted: an item that has `text` is already a chunk, and anything else
    is a list of them.
    """
    items = list(value or ())
    if items and not hasattr(items[0], "text"):
        return list(items[0] or ())
    return items


def _result(raw, *, audio_seconds: float, duration: float) -> TranscriptionResult:
    """Map one openvino-genai result onto the contract every backend returns.

    Kept out of the class and free of any OpenVINO import so it can be tested
    on a machine that cannot run the backend at all — which is every CI runner
    this project has, and every Mac it is developed on.

    Word timings are the load-bearing part. When the runtime provides them they
    are used directly; when it only provides `chunks` — segment-level spans —
    each segment is carried as a single Word. That is coarser than the other
    backends, so the streaming layer trims at segment boundaries rather than
    word boundaries, but it keeps the decode window bounded, which is the
    property that actually matters.
    """
    # A decoded result stringifies to its text, but a batch-shaped one carries
    # `texts`. Checked in that order so an empty transcript stays empty rather
    # than falling through to the repr of the result object.
    texts = getattr(raw, "texts", None)
    text = str(texts[0] if texts else raw).strip()
    chunks = _sequence(getattr(raw, "chunks", None))
    words = _sequence(getattr(raw, "words", None))

    if words:
        timings = tuple(
            Word(text=str(w.text), start=float(w.start_ts), end=float(w.end_ts))
            for w in words
            if w.end_ts is not None and w.start_ts is not None
        )
    else:
        timings = tuple(
            Word(text=str(c.text), start=float(c.start_ts), end=float(c.end_ts))
            for c in chunks
            if c.end_ts is not None and c.start_ts is not None
        )

    return TranscriptionResult(
        text=text,
        audio_seconds=audio_seconds,
        duration_seconds=duration,
        # openvino-genai reports no per-segment log probability, so there is
        # none to average. The streaming layer treats None as "no opinion".
        avg_logprob=None,
        segment_ends=tuple(
            float(c.end_ts) for c in chunks if c.end_ts is not None
        ),
        words=timings,
    )
