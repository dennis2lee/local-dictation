"""The streaming session: PCM in, transcript events out.

Structure
---------
`push_audio` is synchronous and does almost nothing — append to the buffer, feed
the silence tracker, poke an event. All decoding happens on a background task,
so a slow decode never stops the socket reader from draining frames.

Events go onto a queue rather than being returned, because one push can produce
zero events or three (an over-long utterance emits an error, a final transcript
and then starts a fresh utterance).

Invariants this class is responsible for
----------------------------------------
* `revision` strictly increases for the lifetime of the session;
* `stable` only ever grows within an utterance — if the decoder contradicts
  committed text, the utterance is rotated instead of retracted;
* `final: true` is the last event for its utterance;
* nothing in here logs or stores transcript text.
"""

from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Callable
from concurrent.futures import Executor
from typing import Protocol

from app.inference.base import InferenceError, Transcriber, TranscriptionResult
from app.protocol import ServerError, ServerMessage, ServerTranscript
from app.settings import StreamingSettings
from app.streaming.buffer import AudioBuffer, AudioFormatError
from app.streaming.local_agreement import LocalAgreement
from app.streaming.vad import SilenceTracker, create_vad

log = logging.getLogger(__name__)


class MetricsSink(Protocol):
    def observe_decode(self, *, audio_seconds: float, duration_seconds: float) -> None: ...
    def observe_first_partial(self, seconds: float) -> None: ...
    def observe_finalization(self, seconds: float) -> None: ...
    def count_error(self, code: str) -> None: ...
    def count_utterance(self) -> None: ...


class NullMetrics:
    def observe_decode(self, *, audio_seconds: float, duration_seconds: float) -> None: ...
    def observe_first_partial(self, seconds: float) -> None: ...
    def observe_finalization(self, seconds: float) -> None: ...
    def count_error(self, code: str) -> None: ...
    def count_utterance(self) -> None: ...


class StreamingSession:
    def __init__(
        self,
        *,
        session_id: str,
        transcriber: Transcriber,
        settings: StreamingSettings,
        executor: Executor,
        metrics: MetricsSink | None = None,
        clock: Callable[[], float] = time.monotonic,
        vad: object | None = None,
    ) -> None:
        self.session_id = session_id
        self.events: asyncio.Queue[ServerMessage] = asyncio.Queue()

        self._transcriber = transcriber
        self._settings = settings
        self._executor = executor
        self._metrics = metrics or NullMetrics()
        self._clock = clock

        detector = vad if vad is not None else create_vad(settings)
        self._silence = SilenceTracker(detector)  # type: ignore[arg-type]
        self._buffer = AudioBuffer()
        self._agreement = LocalAgreement(settings.agreement_window)

        self._chunk_seconds = settings.chunk_ms / 1000
        self._silence_seconds = settings.silence_ms / 1000

        self._revision = 0
        self._utterance_index = 0
        self._last_emitted: tuple[str, str] | None = None
        self._last_hypothesis = ""

        self._wake = asyncio.Event()
        self._flush_requested = False
        self._flush_done = asyncio.Event()
        self._closing = False
        self._task: asyncio.Task[None] | None = None

        self._first_audio_at: float | None = None
        self._first_partial_seen = False
        self._flush_started_at: float | None = None

        self.decode_count = 0

    # -- lifecycle ---------------------------------------------------------

    async def start(self) -> None:
        if self._task is not None:
            raise RuntimeError("session already started")
        self._task = asyncio.create_task(self._run(), name=f"decode-{self.session_id}")

    async def aclose(self) -> None:
        self._closing = True
        self._wake.set()
        if self._task is not None:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
            self._task = None
        # Dropping the buffer here is the whole retention policy: audio exists
        # only for as long as a session is decoding it.
        self._buffer.reset()
        self._silence.reset()

    async def __aenter__(self) -> StreamingSession:
        await self.start()
        return self

    async def __aexit__(self, *exc_info: object) -> None:
        await self.aclose()

    # -- inbound -----------------------------------------------------------

    @property
    def utterance_id(self) -> str:
        return f"u-{self.session_id}-{self._utterance_index:04d}"

    def push_audio(self, chunk: bytes) -> None:
        """Append one PCM frame. Raises AudioFormatError on a ragged payload."""
        if self._flush_requested or self._closing:
            return  # audio after flush is defined to be ignored
        self._buffer.append(chunk)
        self._silence.push(chunk)
        if self._first_audio_at is None and self._silence.has_speech:
            self._first_audio_at = self._clock()
        self._wake.set()

    async def flush(self, timeout: float = 30.0) -> None:
        """Decode whatever is left and emit a final transcript."""
        if self._flush_requested:
            await self._await_flush(timeout)
            return
        self._flush_requested = True
        self._flush_started_at = self._clock()
        self._wake.set()
        await self._await_flush(timeout)

    async def _await_flush(self, timeout: float) -> None:
        try:
            await asyncio.wait_for(self._flush_done.wait(), timeout)
        except TimeoutError:
            log.warning("flush timed out", extra={"session_id": self.session_id})
            await self.events.put(
                ServerError("inference_failed", "flush timed out before the final decode completed")
            )
            self._flush_done.set()

    # -- decode loop -------------------------------------------------------

    async def _run(self) -> None:
        try:
            while not self._closing:
                await self._wake.wait()
                self._wake.clear()
                while await self._step():
                    if self._closing:
                        break
        except asyncio.CancelledError:
            raise
        except Exception:  # noqa: BLE001 - last line of defence for the task
            log.exception("decode loop failed", extra={"session_id": self.session_id})
            await self.events.put(ServerError("internal_error", "decode loop failed"))
            self._flush_done.set()

    async def _step(self) -> bool:
        """Do at most one unit of work. Returns True if more may be pending."""
        if self._closing:
            return False

        if not self._silence.has_speech:
            # Nothing worth decoding yet. Keep a short pre-roll so the first
            # word is not clipped, and drop the rest rather than growing a
            # buffer of silence toward the max-utterance cap.
            if self._buffer.duration_seconds > self._silence_seconds * 2:
                self._buffer.keep_tail(self._silence_seconds)
            if self._flush_requested:
                await self._finalize()
                self._flush_done.set()
            return False

        if self._buffer.duration_seconds >= self._settings.max_utterance_seconds:
            self._metrics.count_error("utterance_too_long")
            await self.events.put(
                ServerError(
                    "utterance_too_long",
                    f"utterance exceeded {self._settings.max_utterance_seconds}s and was finalized",
                )
            )
            await self._finalize()
            return not self._flush_requested

        if self._flush_requested:
            await self._finalize()
            self._flush_done.set()
            return False

        if (
            self._silence.has_speech
            and self._silence.trailing_silence_seconds >= self._silence_seconds
        ):
            await self._finalize()
            return False

        if self._buffer.undecoded_seconds >= self._chunk_seconds:
            await self._decode_incremental()
            return False

        return False

    async def _decode_incremental(self) -> None:
        pcm = self._buffer.snapshot()
        self._buffer.mark_decoded()

        result = await self._decode(pcm)
        if result is None:
            return

        hypothesis = result.text.strip()
        if not hypothesis:
            return
        self._last_hypothesis = hypothesis

        agreement = self._agreement.update(hypothesis)
        if agreement.conflict:
            # The decoder revised committed text. Close the utterance at what
            # the user already has, then reopen with the new reading.
            await self._close_utterance(agreement.stable)
            self._agreement.reset()
            self._last_hypothesis = hypothesis
            reopened = self._agreement.update(hypothesis)
            self._emit(reopened.stable, reopened.partial, final=False)
            return

        self._emit(agreement.stable, agreement.partial, final=False)

    async def _finalize(self) -> None:
        """End the current utterance, decoding any audio not yet seen."""
        if self._buffer.undecoded_seconds > 0 and self._silence.has_speech:
            pcm = self._buffer.snapshot()
            self._buffer.mark_decoded()
            result = await self._decode(pcm)
            if result is not None and result.text.strip():
                self._last_hypothesis = result.text.strip()

        final_text = self._last_hypothesis or self._agreement.committed
        await self._close_utterance(final_text)
        self._reset_utterance()

    async def _close_utterance(self, final_text: str) -> None:
        """Emit the terminal transcript for the current utterance, if any."""
        if not final_text.strip():
            return

        agreement = self._agreement.commit_all(final_text)
        if agreement.conflict:
            # Committed text cannot be retracted: seal the current utterance
            # with what was already committed, then carry the contradicting
            # reading into a fresh one.
            self._emit(self._agreement.committed, "", final=True)
            self._start_new_utterance()
            self._agreement.reset()
            sealed = self._agreement.commit_all(final_text)
            self._emit(sealed.stable, "", final=True)
        else:
            self._emit(agreement.stable, "", final=True)

        self._metrics.count_utterance()
        if self._flush_started_at is not None:
            self._metrics.observe_finalization(self._clock() - self._flush_started_at)
            self._flush_started_at = None

    def _reset_utterance(self) -> None:
        self._start_new_utterance()
        self._buffer.reset()
        self._silence.reset()
        self._agreement.reset()
        self._last_hypothesis = ""
        self._last_emitted = None

    def _start_new_utterance(self) -> None:
        self._utterance_index += 1

    async def _decode(self, pcm: bytes) -> TranscriptionResult | None:
        if not pcm:
            return None
        loop = asyncio.get_running_loop()
        started = self._clock()
        try:
            result = await loop.run_in_executor(self._executor, self._transcriber.transcribe, pcm)
        except InferenceError as exc:
            self._metrics.count_error("inference_failed")
            log.warning(
                "decode failed", extra={"session_id": self.session_id, "reason": str(exc)}
            )
            await self.events.put(ServerError("inference_failed", str(exc)))
            return None
        except Exception as exc:  # noqa: BLE001 - a backend fault must not kill the session
            self._metrics.count_error("inference_failed")
            log.exception("unexpected decode failure", extra={"session_id": self.session_id})
            await self.events.put(ServerError("inference_failed", f"backend fault: {type(exc).__name__}"))
            return None

        self.decode_count += 1
        self._metrics.observe_decode(
            audio_seconds=result.audio_seconds,
            duration_seconds=result.duration_seconds or (self._clock() - started),
        )
        return result

    def _emit(self, stable: str, partial: str, *, final: bool) -> None:
        if not final and self._last_emitted == (stable, partial):
            return
        self._revision += 1
        self._last_emitted = (stable, partial)
        self.events.put_nowait(
            ServerTranscript(
                utterance_id=self.utterance_id,
                revision=self._revision,
                stable=stable,
                partial=partial,
                final=final,
            )
        )
        if not self._first_partial_seen and self._first_audio_at is not None:
            self._first_partial_seen = True
            self._metrics.observe_first_partial(self._clock() - self._first_audio_at)


__all__ = ["StreamingSession", "AudioFormatError", "MetricsSink", "NullMetrics"]
