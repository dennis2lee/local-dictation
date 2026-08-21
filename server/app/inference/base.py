"""The contract every backend implements."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Protocol, runtime_checkable


class InferenceError(RuntimeError):
    """A decode pass failed. Non-fatal: the session keeps its committed text."""


@dataclass(frozen=True)
class Word:
    """One decoded word and where it sits in the audio that was passed in."""

    text: str
    start: float
    end: float


@dataclass(frozen=True)
class TranscriptionResult:
    """One decode of the whole current utterance buffer.

    ``text`` is the complete hypothesis for the audio it was given, not an
    increment. The streaming layer is what turns a sequence of these into a
    monotonically growing committed prefix.
    """

    text: str
    #: Seconds of audio the backend actually consumed.
    audio_seconds: float = 0.0
    #: Wall-clock seconds the decode took. `duration / audio_seconds` is RTF.
    duration_seconds: float = 0.0
    #: Mean log-probability when the backend reports one; None otherwise.
    avg_logprob: float | None = None
    #: Segment end times in seconds.
    segment_ends: tuple[float, ...] = field(default_factory=tuple)
    #: Word-level timings. This is what lets the streaming layer drop audio it
    #: has already committed, which is the difference between a decode window
    #: that grows without bound and one that stays flat.
    words: tuple[Word, ...] = field(default_factory=tuple)

    @property
    def real_time_factor(self) -> float | None:
        if self.audio_seconds <= 0:
            return None
        return self.duration_seconds / self.audio_seconds


@runtime_checkable
class Transcriber(Protocol):
    """Blocking, CPU-bound. Callers run it off the event loop."""

    @property
    def name(self) -> str:
        """Model identifier reported to clients in the `ready` event."""

    @property
    def language(self) -> str:
        """The single language this instance is pinned to."""

    def warmup(self) -> None:
        """Decode a short synthetic sample so the first real utterance is not
        paying for lazy weight loading."""

    def transcribe(self, pcm: bytes, *, prompt: str = "") -> TranscriptionResult:
        """Transcribe 16 kHz mono signed 16-bit little-endian PCM.

        `prompt` carries the text already committed for this utterance, so a
        decode of a trimmed window still knows what came before it.

        Raises InferenceError on a recoverable failure.
        """

    def close(self) -> None:
        """Release model resources."""
