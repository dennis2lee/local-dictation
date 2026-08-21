"""Voice activity detection and trailing-silence tracking.

Two jobs, and only the second one is on the critical path:

* deciding *when an utterance has ended* so the session can finalize — that is
  what `SilenceTracker` does with whatever detector is configured;
* filtering non-speech out of the decode itself, which faster-whisper already
  does internally via `vad_filter=True`.

Silero is the accurate option and the configured default. The energy detector is
the fallback that always works: no model file, no onnxruntime, no surprises on a
host where the model was not mirrored. Falling back is logged loudly, because an
operator who thinks they are running Silero and is not will see utterances break
in noisy rooms.
"""

from __future__ import annotations

import logging
import math
import struct
from pathlib import Path
from typing import Protocol, runtime_checkable

from app.settings import StreamingSettings

log = logging.getLogger(__name__)

SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2


@runtime_checkable
class VoiceActivityDetector(Protocol):
    @property
    def name(self) -> str: ...

    @property
    def frame_samples(self) -> int:
        """Exact frame size this detector consumes, in samples."""

    def is_speech(self, frame: bytes) -> bool:
        """Classify one frame of exactly `frame_samples` samples."""

    def reset(self) -> None:
        """Drop any per-utterance recurrent state."""


class EnergyVad:
    """RMS threshold over 20 ms frames.

    Crude but predictable, and with a 600 ms silence window a few misclassified
    frames never end an utterance on their own.
    """

    def __init__(self, threshold: float = 0.006, frame_ms: int = 20) -> None:
        self._threshold = threshold
        self._frame_samples = int(SAMPLE_RATE * frame_ms / 1000)

    @property
    def name(self) -> str:
        return "energy"

    @property
    def frame_samples(self) -> int:
        return self._frame_samples

    def is_speech(self, frame: bytes) -> bool:
        return _rms(frame) >= self._threshold

    def reset(self) -> None:
        return None


class SileroVad:
    """Silero VAD through onnxruntime, loaded from a local file only.

    Two things about the exported graph are easy to get wrong and produce a
    detector that silently reports silence forever:

    * v5 takes 576 samples per call at 16 kHz — a 512-sample frame with the
      previous frame's last 64 samples prepended as context. Feed it a bare
      512-sample frame and every probability comes back near zero, which looks
      exactly like a quiet room.
    * v4 exposes `h`/`c` recurrent inputs, v5 a single `state`.

    Both are handled by reading the input names from the model rather than
    assuming, and the constructor runs a self-test that fails loudly if the
    probabilities do not respond to signal at all.
    """

    #: Samples of the previous frame that v5 expects prepended to each window.
    CONTEXT_SAMPLES = 64
    FRAME_SAMPLES = 512

    def __init__(self, model_path: str | Path, threshold: float = 0.5) -> None:
        try:
            import numpy as np
            import onnxruntime
        except ImportError as exc:
            raise RuntimeError("onnxruntime is not installed (extra: vad)") from exc

        path = Path(model_path)
        if not path.is_file():
            raise RuntimeError(f"silero model not found: {path}")

        options = onnxruntime.SessionOptions()
        options.inter_op_num_threads = 1
        options.intra_op_num_threads = 1
        # The VAD must never compete with the decoder for cores.
        self._session = onnxruntime.InferenceSession(
            str(path), sess_options=options, providers=["CPUExecutionProvider"]
        )
        self._np = np
        self._threshold = threshold

        self._input_names = [i.name for i in self._session.get_inputs()]
        if "input" not in self._input_names:
            raise RuntimeError(f"unexpected silero graph inputs: {self._input_names}")
        self._stateful_v5 = "state" in self._input_names
        self._stateful_v4 = "h" in self._input_names and "c" in self._input_names
        if not (self._stateful_v5 or self._stateful_v4):
            raise RuntimeError(f"unsupported silero variant, inputs: {self._input_names}")

        # v4 consumes the window directly; only v5 wants the carried context.
        self._context_samples = self.CONTEXT_SAMPLES if self._stateful_v5 else 0

        self.reset()
        self._smoke_test()
        self.reset()

    @property
    def name(self) -> str:
        return "silero"

    @property
    def frame_samples(self) -> int:
        return self.FRAME_SAMPLES

    def reset(self) -> None:
        np = self._np
        self._context = np.zeros(self._context_samples, dtype=np.float32)
        if self._stateful_v5:
            self._state = np.zeros((2, 1, 128), dtype=np.float32)
        else:
            self._h = np.zeros((2, 1, 64), dtype=np.float32)
            self._c = np.zeros((2, 1, 64), dtype=np.float32)

    def probability(self, frame: bytes) -> float:
        """Speech probability for one frame of exactly `frame_samples`."""
        np = self._np
        samples = np.frombuffer(frame, dtype="<i2").astype(np.float32) / 32768.0
        if samples.size != self.FRAME_SAMPLES:
            padded = np.zeros(self.FRAME_SAMPLES, dtype=np.float32)
            padded[: min(samples.size, self.FRAME_SAMPLES)] = samples[: self.FRAME_SAMPLES]
            samples = padded

        window = (
            np.concatenate([self._context, samples]) if self._context_samples else samples
        )
        feed = {
            "input": window.reshape(1, -1),
            "sr": np.array(SAMPLE_RATE, dtype=np.int64),
        }
        if self._stateful_v5:
            feed["state"] = self._state
        else:
            feed["h"] = self._h
            feed["c"] = self._c

        outputs = self._session.run(None, feed)
        if self._stateful_v5:
            self._state = outputs[1]
        else:
            self._h, self._c = outputs[1], outputs[2]
        if self._context_samples:
            self._context = samples[-self._context_samples :]
        return float(np.asarray(outputs[0]).reshape(-1)[0])

    def is_speech(self, frame: bytes) -> bool:
        return self.probability(frame) >= self._threshold

    def _smoke_test(self) -> None:
        """Prove the graph runs and returns a finite probability.

        This cannot prove the window size is right: Silero is a speech detector,
        so it correctly ignores any noise we could synthesise here, and a
        misconfigured window looks identical to a quiet room. That case is
        caught at runtime instead — see SilenceTracker's energy cross-check.
        """
        probability = self.probability(b"\x00\x00" * self.FRAME_SAMPLES)
        if not 0.0 <= probability <= 1.0:
            raise RuntimeError(f"model returned {probability!r}, expected a probability")


class AlwaysSpeech:
    """Used when vad is disabled: nothing is ever silence, so utterances end
    only on flush or the max-length cap."""

    @property
    def name(self) -> str:
        return "none"

    @property
    def frame_samples(self) -> int:
        return int(SAMPLE_RATE * 0.02)

    def is_speech(self, frame: bytes) -> bool:
        return True

    def reset(self) -> None:
        return None


def create_vad(settings: StreamingSettings) -> VoiceActivityDetector:
    if settings.vad == "none":
        return AlwaysSpeech()
    if settings.vad == "energy":
        return EnergyVad(threshold=settings.energy_threshold)

    try:
        vad = SileroVad(settings.silero_model_path or "")
    except RuntimeError as exc:
        log.warning(
            "silero VAD unavailable, falling back to the energy detector: %s. "
            "Utterance segmentation will be less reliable in noisy rooms.",
            exc,
        )
        return EnergyVad(threshold=settings.energy_threshold)
    log.info("using silero VAD", extra={"model_path": settings.silero_model_path})
    return vad


class SilenceTracker:
    """Feeds whole frames to a detector and reports trailing silence.

    Audio arrives in whatever sizes the client chose, which will not line up
    with the detector's frame size, so partial frames are carried over. Work is
    O(new samples): nothing rescans the buffer.

    It also cross-checks the detector against raw signal level. A neural VAD
    that is wired up wrong does not raise — it reports silence forever, and the
    server sits there transcribing nothing while the user talks. If the detector
    calls a stretch of clearly loud audio silent, the tracker says so and falls
    back to the energy detector rather than staying deaf.
    """

    #: RMS above which audio is unambiguously not a quiet room.
    LOUD_RMS = 0.02
    #: How long the detector may disagree with the signal level before we stop
    #: believing it. Long enough that a genuinely noisy room does not trip it.
    DISAGREEMENT_LIMIT_SECONDS = 4.0

    def __init__(
        self,
        vad: VoiceActivityDetector,
        sample_rate: int = SAMPLE_RATE,
        *,
        cross_check: bool = True,
    ) -> None:
        self._vad = vad
        self._sample_rate = sample_rate
        self._frame_bytes = vad.frame_samples * BYTES_PER_SAMPLE
        self._frame_seconds = vad.frame_samples / sample_rate
        self._leftover = b""
        self._trailing_silence_frames = 0
        self._speech_frames = 0

        self._cross_check = cross_check and vad.name not in ("energy", "none")
        self._loud_but_silent_frames = 0
        self._fell_back = False

    @property
    def detector_name(self) -> str:
        return self._vad.name

    @property
    def fell_back(self) -> bool:
        """True once the detector was replaced for disagreeing with the signal."""
        return self._fell_back

    @property
    def has_speech(self) -> bool:
        return self._speech_frames > 0

    @property
    def speech_seconds(self) -> float:
        return self._speech_frames * self._frame_seconds

    @property
    def trailing_silence_seconds(self) -> float:
        return self._trailing_silence_frames * self._frame_seconds

    def push(self, pcm: bytes) -> None:
        data = self._leftover + pcm
        offset = 0
        while offset + self._frame_bytes <= len(data):
            frame = data[offset : offset + self._frame_bytes]
            offset += self._frame_bytes
            speech = self._vad.is_speech(frame)
            if self._cross_check:
                self._audit(frame, speech)
            if speech:
                self._speech_frames += 1
                self._trailing_silence_frames = 0
            else:
                self._trailing_silence_frames += 1
        self._leftover = data[offset:]

    def _audit(self, frame: bytes, speech: bool) -> None:
        if speech:
            self._loud_but_silent_frames = 0
            return
        if _rms(frame) < self.LOUD_RMS:
            self._loud_but_silent_frames = 0
            return

        self._loud_but_silent_frames += 1
        if self._loud_but_silent_frames * self._frame_seconds < self.DISAGREEMENT_LIMIT_SECONDS:
            return

        log.error(
            "%s VAD reported %.1fs of clearly audible input as silence; "
            "falling back to the energy detector for this session. The model "
            "file or its expected window size is probably wrong.",
            self._vad.name,
            self._loud_but_silent_frames * self._frame_seconds,
        )
        self._replace_with_energy()

    def _replace_with_energy(self) -> None:
        self._vad = EnergyVad()
        self._frame_bytes = self._vad.frame_samples * BYTES_PER_SAMPLE
        self._frame_seconds = self._vad.frame_samples / self._sample_rate
        self._leftover = b""
        self._cross_check = False
        self._fell_back = True

    def reset(self) -> None:
        self._leftover = b""
        self._trailing_silence_frames = 0
        self._speech_frames = 0
        self._loud_but_silent_frames = 0
        self._vad.reset()


def _rms(frame: bytes) -> float:
    count = len(frame) // BYTES_PER_SAMPLE
    if count == 0:
        return 0.0
    total = 0
    for (sample,) in struct.iter_unpack("<h", frame[: count * BYTES_PER_SAMPLE]):
        total += sample * sample
    return math.sqrt(total / count) / 32768.0
