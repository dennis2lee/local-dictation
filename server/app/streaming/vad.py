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
        count = len(frame) // BYTES_PER_SAMPLE
        if count == 0:
            return False
        total = 0
        for (sample,) in struct.iter_unpack("<h", frame[: count * BYTES_PER_SAMPLE]):
            total += sample * sample
        rms = math.sqrt(total / count) / 32768.0
        return rms >= self._threshold

    def reset(self) -> None:
        return None


class SileroVad:
    """Silero VAD through onnxruntime, loaded from a local file only.

    The exported graph differs between Silero v4 (`h`/`c` inputs) and v5
    (`state`), so the input names are read from the model rather than assumed.
    Anything unexpected raises at construction time, where `create_vad` turns it
    into a fallback instead of a mid-session failure.
    """

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

        self._frame_samples = 512  # both v4 and v5 accept 512 at 16 kHz
        self.reset()
        # Prove the graph actually runs before the first utterance depends on it.
        self.is_speech(b"\x00\x00" * self._frame_samples)
        self.reset()

    @property
    def name(self) -> str:
        return "silero"

    @property
    def frame_samples(self) -> int:
        return self._frame_samples

    def reset(self) -> None:
        np = self._np
        if self._stateful_v5:
            self._state = np.zeros((2, 1, 128), dtype=np.float32)
        else:
            self._h = np.zeros((2, 1, 64), dtype=np.float32)
            self._c = np.zeros((2, 1, 64), dtype=np.float32)

    def is_speech(self, frame: bytes) -> bool:
        np = self._np
        samples = np.frombuffer(frame, dtype="<i2").astype(np.float32) / 32768.0
        if samples.size != self._frame_samples:
            padded = np.zeros(self._frame_samples, dtype=np.float32)
            padded[: min(samples.size, self._frame_samples)] = samples[: self._frame_samples]
            samples = padded

        feed = {
            "input": samples.reshape(1, -1),
            "sr": np.array(SAMPLE_RATE, dtype=np.int64),
        }
        if self._stateful_v5:
            feed["state"] = self._state
        else:
            feed["h"] = self._h
            feed["c"] = self._c

        outputs = self._session.run(None, feed)
        probability = float(np.asarray(outputs[0]).reshape(-1)[0])
        if self._stateful_v5:
            self._state = outputs[1]
        else:
            self._h, self._c = outputs[1], outputs[2]
        return probability >= self._threshold


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
    """

    def __init__(self, vad: VoiceActivityDetector, sample_rate: int = SAMPLE_RATE) -> None:
        self._vad = vad
        self._sample_rate = sample_rate
        self._frame_bytes = vad.frame_samples * BYTES_PER_SAMPLE
        self._frame_seconds = vad.frame_samples / sample_rate
        self._leftover = b""
        self._trailing_silence_frames = 0
        self._speech_frames = 0

    @property
    def detector_name(self) -> str:
        return self._vad.name

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
            if self._vad.is_speech(frame):
                self._speech_frames += 1
                self._trailing_silence_frames = 0
            else:
                self._trailing_silence_frames += 1
        self._leftover = data[offset:]

    def reset(self) -> None:
        self._leftover = b""
        self._trailing_silence_frames = 0
        self._speech_frames = 0
        self._vad.reset()
