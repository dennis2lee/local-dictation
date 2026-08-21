"""A deterministic backend for tests and for `--dry-run` smoke checks.

It emits words from a fixed script in proportion to how much audio it has been
given, which is enough to exercise the LocalAgreement policy, the revision
counter and every event path without a 3 GB model on disk.
"""

from __future__ import annotations

import threading
import time

from app.inference.base import InferenceError, TranscriptionResult, Word

SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2

DEFAULT_SCRIPT = {
    "ko": "오늘 오후 세 시에 회의를 시작합니다 준비해 주세요".split(),
    "en": "the quick brown fox jumps over the lazy dog".split(),
}


class FakeTranscriber:
    """Reveals one scripted word per `seconds_per_word` of audio."""

    def __init__(
        self,
        language: str = "ko",
        *,
        script: list[str] | None = None,
        seconds_per_word: float = 0.5,
        latency_seconds: float = 0.0,
        fail_on_call: int | None = None,
    ) -> None:
        self._language = language
        self._script = list(script if script is not None else DEFAULT_SCRIPT.get(language, DEFAULT_SCRIPT["en"]))
        self._seconds_per_word = seconds_per_word
        self._latency = latency_seconds
        self._fail_on_call = fail_on_call
        self._lock = threading.Lock()
        self.calls = 0
        self.last_prompt = ""
        self.warmed_up = False
        self.closed = False

    @property
    def name(self) -> str:
        return "fake"

    @property
    def language(self) -> str:
        return self._language

    def warmup(self) -> None:
        self.warmed_up = True

    def transcribe(self, pcm: bytes, *, prompt: str = "") -> TranscriptionResult:
        self.last_prompt = prompt
        with self._lock:
            self.calls += 1
            call = self.calls
        if self._latency:
            time.sleep(self._latency)
        if self._fail_on_call is not None and call == self._fail_on_call:
            raise InferenceError("scripted failure")

        audio_seconds = len(pcm) / (SAMPLE_RATE * BYTES_PER_SAMPLE)
        count = min(len(self._script), int(audio_seconds / self._seconds_per_word))
        spoken = self._script[:count]
        words = tuple(
            Word(
                text=(" " if index else "") + word,
                start=index * self._seconds_per_word,
                end=(index + 1) * self._seconds_per_word,
            )
            for index, word in enumerate(spoken)
        )
        return TranscriptionResult(
            text=" ".join(spoken),
            audio_seconds=audio_seconds,
            duration_seconds=self._latency,
            avg_logprob=-0.2,
            words=words,
        )

    def close(self) -> None:
        self.closed = True
