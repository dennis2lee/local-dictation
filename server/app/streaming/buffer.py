"""The per-utterance audio buffer.

Whisper is not an incremental decoder: every pass re-reads the whole utterance.
So the buffer holds one utterance, and `undecoded_seconds` is what tells the
session when enough new audio has arrived to be worth another pass.
"""

from __future__ import annotations

SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2


class AudioFormatError(ValueError):
    """Payload is not whole 16-bit samples."""


class AudioBuffer:
    def __init__(self, sample_rate: int = SAMPLE_RATE) -> None:
        self._sample_rate = sample_rate
        self._data = bytearray()
        self._decoded_bytes = 0
        #: Total audio ever appended, across utterances. Session metrics only.
        self.total_bytes = 0

    def append(self, pcm: bytes) -> None:
        if len(pcm) % BYTES_PER_SAMPLE:
            raise AudioFormatError(
                f"expected whole 16-bit samples, got {len(pcm)} bytes"
            )
        self._data += pcm
        self.total_bytes += len(pcm)

    def snapshot(self) -> bytes:
        """An immutable copy for handing to a worker thread.

        The copy matters: the decode runs in another thread while the reader
        keeps appending, and `bytes(bytearray)` under mutation is a data race
        waiting to happen.
        """
        return bytes(self._data)

    def mark_decoded(self) -> None:
        self._decoded_bytes = len(self._data)

    def keep_tail(self, seconds: float) -> None:
        """Drop everything but the last `seconds` of audio.

        Used to bound the buffer while the room is silent, so a client that
        streams for ten minutes without speaking does not accumulate ten
        minutes of PCM waiting for a decode that will never be worth running.
        The retained tail is marked as already decoded.
        """
        keep_bytes = int(seconds * self._sample_rate) * BYTES_PER_SAMPLE
        if keep_bytes < len(self._data):
            del self._data[: len(self._data) - keep_bytes]
        self._decoded_bytes = len(self._data)

    def reset(self) -> None:
        self._data.clear()
        self._decoded_bytes = 0

    @property
    def byte_length(self) -> int:
        return len(self._data)

    @property
    def is_empty(self) -> bool:
        return not self._data

    @property
    def duration_seconds(self) -> float:
        return len(self._data) / (self._sample_rate * BYTES_PER_SAMPLE)

    @property
    def undecoded_seconds(self) -> float:
        return (len(self._data) - self._decoded_bytes) / (self._sample_rate * BYTES_PER_SAMPLE)

    def __len__(self) -> int:
        return len(self._data)
