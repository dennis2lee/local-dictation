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
from functools import partial
from concurrent.futures import Executor
from typing import Protocol

from app.inference.base import InferenceError, Transcriber, TranscriptionResult, Word
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
        draft: Transcriber | None = None,
    ) -> None:
        self.session_id = session_id
        self.events: asyncio.Queue[ServerMessage] = asyncio.Queue()

        self._transcriber = transcriber
        # When a draft model is configured, incremental passes run on it and
        # its output is shown but never committed: the accurate model decodes
        # the utterance once at the end and that is what becomes real text.
        self._draft = draft
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
        #: Latest hypothesis for the audio currently in the buffer.
        self._window_hypothesis = ""
        #: Text committed for this utterance whose audio has been trimmed away.
        #: Emitted `stable` is always this plus whatever the window has agreed.
        self._committed_prefix = ""
        #: Tail of the committed text, handed to the decoder so a trimmed window
        #: still knows what sentence it is in the middle of.
        self._prompt = ""

        self._wake = asyncio.Event()
        self._flush_requested = False
        self._flush_done = asyncio.Event()
        self._closing = False
        self._task: asyncio.Task[None] | None = None

        self._first_audio_at: float | None = None
        self._first_partial_seen = False
        self._flush_started_at: float | None = None

        self.decode_count = 0
        self.draft_decode_count = 0

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

        result = await self._decode(pcm, self._draft or self._transcriber)
        if result is None:
            return

        hypothesis = result.text.strip()
        if not hypothesis:
            return
        self._window_hypothesis = hypothesis

        agreement = self._agreement.update(hypothesis)

        if self._draft is not None:
            # Draft text is provisional by construction, so a revision is not a
            # conflict — nothing has been committed from it. Show the whole
            # window hypothesis as partial and let the accurate model decide
            # what the sentence actually was.
            if agreement.conflict:
                self._agreement.reset()
                self._agreement.update(hypothesis)
            self._emit(self._committed_prefix, hypothesis, final=False)
            self._trim_decoded_audio(result)
            return

        if agreement.conflict:
            # The decoder revised committed text. Close the utterance at what
            # the user already has, then reopen with the new reading. The audio
            # stays: it belongs to the new utterance.
            await self._close_utterance(self._agreement.committed)
            self._start_new_utterance()
            self._committed_prefix = ""
            self._prompt = ""
            self._agreement.reset()
            self._last_emitted = None
            reopened = self._agreement.update(hypothesis)
            self._emit(self._committed_prefix + reopened.stable, reopened.partial, final=False)
            return

        self._emit(self._committed_prefix + agreement.stable, agreement.partial, final=False)
        self._trim_decoded_audio(result)

    def _trim_decoded_audio(self, result: TranscriptionResult) -> None:
        """Drop audio whose text is already committed.

        Without this the decode window grows for as long as someone keeps
        talking, and because Whisper re-reads its whole input every pass the
        cost per pass grows with it. Past roughly ten seconds of continuous
        speech on CPU the decoder stops keeping up and never recovers — which is
        exactly the latency risk the project plan flags as its largest.

        The committed text is carried forward as a prompt instead, so the
        decoder still has the context it needs to punctuate and capitalise.
        """
        if self._buffer.duration_seconds < self._settings.max_window_seconds:
            return
        committed = self._agreement.committed
        if not committed.strip():
            return

        cut = _word_boundary_seconds(result.words, committed)
        if cut is None or cut <= 0.1:
            return

        self._buffer.trim_before(cut)
        self._committed_prefix += committed
        self._prompt = self._committed_prefix[-_PROMPT_CHARACTERS:]
        self._agreement.reset()
        self._window_hypothesis = ""
        log.debug(
            "trimmed decode window",
            extra={
                "session_id": self.session_id,
                "trimmed_seconds": round(cut, 2),
                "window_seconds": round(self._buffer.duration_seconds, 2),
            },
        )

    async def _finalize(self) -> None:
        """End the current utterance, decoding any audio not yet seen."""
        # In draft mode the accurate model has not seen this audio at all, so it
        # must run even when every sample was already decoded by the draft.
        # With a single model, a pass over audio nothing has been added to would
        # just reproduce the last hypothesis at full cost.
        needs_accurate_pass = self._silence.has_speech and (
            self._draft is not None or self._buffer.undecoded_seconds > 0
        )
        if needs_accurate_pass:
            pcm = self._buffer.snapshot()
            self._buffer.mark_decoded()
            # Always the accurate model, even in draft mode: this is the pass
            # whose output the user keeps.
            result = await self._decode(pcm, self._transcriber)
            if result is not None and result.text.strip():
                self._window_hypothesis = result.text.strip()

        if self._draft is not None:
            # The window agreement only ever held draft text, and none of it
            # reached the client as `stable`. Clearing it means the accurate
            # reading cannot "conflict" with a guess nobody committed.
            self._agreement.reset()

        window_text = self._window_hypothesis or self._agreement.committed
        await self._close_utterance(window_text)
        self._reset_utterance()

    async def _close_utterance(self, window_text: str) -> None:
        """Emit the terminal transcript for the current utterance, if any."""
        if not (self._committed_prefix + window_text).strip():
            return

        agreement = self._agreement.commit_all(window_text)
        if agreement.conflict:
            # Committed text cannot be retracted: seal the current utterance
            # with what was already committed, then carry the contradicting
            # reading into a fresh one.
            self._emit(self._committed_prefix + self._agreement.committed, "", final=True)
            self._start_new_utterance()
            self._committed_prefix = ""
            self._prompt = ""
            self._agreement.reset()
            self._last_emitted = None
            sealed = self._agreement.commit_all(window_text)
            self._emit(sealed.stable, "", final=True)
        else:
            self._emit(self._committed_prefix + agreement.stable, "", final=True)

        self._metrics.count_utterance()
        if self._flush_started_at is not None:
            self._metrics.observe_finalization(self._clock() - self._flush_started_at)
            self._flush_started_at = None

    def _reset_utterance(self) -> None:
        self._start_new_utterance()
        self._buffer.reset()
        self._silence.reset()
        self._agreement.reset()
        self._window_hypothesis = ""
        self._committed_prefix = ""
        self._prompt = ""
        self._last_emitted = None

    def _start_new_utterance(self) -> None:
        self._utterance_index += 1

    async def _decode(self, pcm: bytes, transcriber: Transcriber) -> TranscriptionResult | None:
        if not pcm:
            return None
        loop = asyncio.get_running_loop()
        started = self._clock()
        prompt = self._prompt
        try:
            result = await loop.run_in_executor(
                self._executor, partial(transcriber.transcribe, pcm, prompt=prompt)
            )
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
        if transcriber is self._draft:
            self.draft_decode_count += 1
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


#: How much committed text to hand the decoder as context. Whisper charges the
#: prompt against its context window, so this is deliberately short.
_PROMPT_CHARACTERS = 200


def _word_boundary_seconds(words: tuple[Word, ...], committed: str) -> float | None:
    """Audio time at which `committed` ends, per the decoder's own word timings.

    Returns None when the words do not cover the committed text, which happens
    if a backend reports no timings at all. Trimming is then skipped — slower,
    but never wrong.
    """
    target = len(committed.strip())
    if target == 0 or not words:
        return None
    consumed = ""
    for word in words:
        consumed += word.text
        if len(consumed.strip()) >= target:
            return word.end
    return None


__all__ = ["StreamingSession", "AudioFormatError", "MetricsSink", "NullMetrics"]
