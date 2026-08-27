"""Buffer, VAD, LocalAgreement and the session that ties them together."""

from __future__ import annotations

import asyncio

import pytest

from app.inference.base import TranscriptionResult, Word
from app.inference.fake import FakeTranscriber
from app.protocol import ServerError, ServerTranscript
from app.settings import StreamingSettings
from app.streaming.buffer import AudioBuffer, AudioFormatError
from app.streaming.local_agreement import LocalAgreement, tokenize
from app.streaming.session import StreamingSession
from app.streaming.stitch import MAX_REPEAT_CHARACTERS, drop_repeated_prefix, repeated_prefix
from app.streaming.vad import EnergyVad, SilenceTracker
from tests.conftest import SAMPLE_RATE, silence, speech

# -- buffer ----------------------------------------------------------------


def test_buffer_rejects_ragged_pcm():
    buffer = AudioBuffer()
    with pytest.raises(AudioFormatError):
        buffer.append(b"\x00\x00\x00")


def test_buffer_tracks_undecoded_audio():
    buffer = AudioBuffer()
    buffer.append(speech(1.0))
    assert buffer.undecoded_seconds == pytest.approx(1.0)
    buffer.mark_decoded()
    assert buffer.undecoded_seconds == 0
    buffer.append(speech(0.5))
    assert buffer.undecoded_seconds == pytest.approx(0.5)
    assert buffer.duration_seconds == pytest.approx(1.5)


def test_keep_tail_bounds_a_silent_buffer():
    buffer = AudioBuffer()
    buffer.append(silence(10.0))
    buffer.keep_tail(0.6)
    assert buffer.duration_seconds == pytest.approx(0.6)
    assert buffer.undecoded_seconds == 0


def test_snapshot_is_isolated_from_later_appends():
    buffer = AudioBuffer()
    buffer.append(speech(0.1))
    snapshot = buffer.snapshot()
    buffer.append(speech(0.1))
    assert len(snapshot) == len(speech(0.1))


# -- tokenizer / LocalAgreement --------------------------------------------


@pytest.mark.parametrize("text", ["", "a", "hello world", "  leading", "trailing ", "오늘 오후 세"])
def test_tokenize_round_trips(text):
    assert "".join(tokenize(text)) == text


def test_nothing_commits_before_the_window_is_full():
    agreement = LocalAgreement(2)
    assert agreement.update("오늘 오후").stable == ""
    assert agreement.update("오늘 오후 세").stable == "오늘 "


def test_window_of_three_is_more_conservative():
    agreement = LocalAgreement(3)
    for hypothesis in ("the quick", "the quick brown"):
        assert agreement.update(hypothesis).stable == ""
    assert agreement.update("the quick brown fox").stable == "the "


def test_committed_prefix_only_grows():
    agreement = LocalAgreement(2)
    seen = ""
    for hypothesis in [
        "오늘",
        "오늘 오후",
        "오늘 오후 세",
        "오늘 오후 세 시에",
        "오늘 오후 세 시에 회의를",
    ]:
        result = agreement.update(hypothesis)
        assert result.stable.startswith(seen)
        assert result.stable + result.partial == hypothesis
        seen = result.stable


def test_a_trailing_word_is_never_committed():
    # "세" could still become "세시"; holding it back costs one pass.
    agreement = LocalAgreement(2)
    agreement.update("오늘 오후 세")
    result = agreement.update("오늘 오후 세")
    assert result.stable == "오늘 오후 "


def test_respacing_is_not_a_conflict():
    agreement = LocalAgreement(2)
    agreement.update("hello world today")
    agreement.update("hello world today")
    result = agreement.update("hello  world today now")
    assert not result.conflict
    assert result.stable + result.partial == "hello  world today now"


def test_a_real_revision_is_a_conflict():
    agreement = LocalAgreement(2)
    agreement.update("오늘 오후 세")
    agreement.update("오늘 오후 세 시에")
    result = agreement.update("어제 오후 세 시에")
    assert result.conflict
    assert result.stable == "오늘 오후 "  # unchanged: committed text is never retracted


def test_commit_all_reports_conflict_without_mutating():
    agreement = LocalAgreement(2)
    agreement.update("a b c")
    agreement.update("a b c")
    before = agreement.committed
    assert agreement.commit_all("x y z").conflict
    assert agreement.committed == before


# -- VAD -------------------------------------------------------------------


def test_energy_vad_separates_speech_from_silence():
    vad = EnergyVad()
    frame = vad.frame_samples * 2
    assert vad.is_speech(speech(1.0)[:frame])
    assert not vad.is_speech(silence(1.0)[:frame])


def test_silence_tracker_measures_the_trailing_gap():
    tracker = SilenceTracker(EnergyVad())
    tracker.push(speech(1.0))
    assert tracker.has_speech
    assert tracker.trailing_silence_seconds == 0
    tracker.push(silence(0.5))
    assert tracker.trailing_silence_seconds == pytest.approx(0.5, abs=0.03)
    tracker.push(speech(0.1))
    assert tracker.trailing_silence_seconds == 0


def test_silence_tracker_handles_frames_that_do_not_align():
    tracker = SilenceTracker(EnergyVad())
    chunk = speech(1.0)
    for offset in range(0, len(chunk), 666 * 2):  # deliberately ragged
        tracker.push(chunk[offset : offset + 666 * 2])
    assert tracker.has_speech
    assert tracker.speech_seconds == pytest.approx(1.0, abs=0.05)


# -- session ---------------------------------------------------------------


async def drain(session: StreamingSession) -> list:
    events = []
    while not session.events.empty():
        events.append(session.events.get_nowait())
    return events


async def feed(session: StreamingSession, chunks, pause: float = 0.02) -> None:
    for chunk in chunks:
        session.push_audio(chunk)
        await asyncio.sleep(pause)


async def test_session_emits_growing_partials_then_one_final(
    streaming_settings, transcriber, executor
):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 15)
        await feed(session, [silence(0.2)] * 4, pause=0.04)
        await asyncio.sleep(0.2)
        events = await drain(session)

    transcripts = [e for e in events if isinstance(e, ServerTranscript)]
    assert transcripts, "no transcripts produced"
    assert sum(1 for e in transcripts if e.final) == 1
    assert transcripts[-1].final


async def test_revisions_strictly_increase(streaming_settings, transcriber, executor):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 12)
        await session.flush()
        events = await drain(session)

    revisions = [e.revision for e in events if isinstance(e, ServerTranscript)]
    assert revisions == sorted(set(revisions))
    assert all(b > a for a, b in zip(revisions, revisions[1:]))


async def test_stable_is_append_only_within_an_utterance(
    streaming_settings, transcriber, executor
):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 15)
        await session.flush()
        events = await drain(session)

    per_utterance: dict[str, str] = {}
    for event in events:
        if not isinstance(event, ServerTranscript):
            continue
        previous = per_utterance.get(event.utterance_id, "")
        assert event.stable.startswith(previous), (
            f"{event.utterance_id}: {previous!r} -> {event.stable!r} retracts committed text"
        )
        per_utterance[event.utterance_id] = event.stable


async def test_final_is_terminal_for_its_utterance(streaming_settings, transcriber, executor):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 10)
        await feed(session, [silence(0.2)] * 4, pause=0.04)
        await feed(session, [speech(0.2)] * 10)
        await session.flush()
        events = await drain(session)

    finalized: set[str] = set()
    for event in events:
        if not isinstance(event, ServerTranscript):
            continue
        assert event.utterance_id not in finalized, "event after final for the same utterance"
        if event.final:
            finalized.add(event.utterance_id)
    assert len(finalized) >= 2, "silence did not split the audio into two utterances"


async def test_silence_alone_produces_nothing(streaming_settings, transcriber, executor):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [silence(0.2)] * 20)
        await session.flush()
        events = await drain(session)

    assert events == []
    assert session.decode_count == 0, "burned CPU decoding silence"


async def test_audio_after_flush_is_ignored(streaming_settings, transcriber, executor):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 8)
        await session.flush()
        before = await drain(session)
        session.push_audio(speech(1.0))
        await asyncio.sleep(0.2)
        after = await drain(session)

    assert any(e.final for e in before if isinstance(e, ServerTranscript))
    assert after == []


async def test_an_over_long_utterance_is_force_finalized(transcriber, executor):
    settings = StreamingSettings(
        chunk_ms=200, silence_ms=5000, max_utterance_seconds=5, vad="energy"
    )
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=settings, executor=executor
    ) as session:
        await feed(session, [speech(1.0)] * 7, pause=0.05)
        await asyncio.sleep(0.2)
        events = await drain(session)

    errors = [e for e in events if isinstance(e, ServerError)]
    assert any(e.code == "utterance_too_long" for e in errors)
    assert not any(e.fatal for e in errors), "the session should survive a long utterance"
    assert any(e.final for e in events if isinstance(e, ServerTranscript))


async def test_a_failed_decode_is_reported_but_not_fatal(streaming_settings, executor):
    failing = FakeTranscriber("ko", seconds_per_word=0.3, fail_on_call=1)
    async with StreamingSession(
        session_id="s-1", transcriber=failing, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 12)
        await session.flush()
        events = await drain(session)

    errors = [e for e in events if isinstance(e, ServerError)]
    assert [e.code for e in errors] == ["inference_failed"]
    assert not errors[0].fatal
    assert any(isinstance(e, ServerTranscript) and e.final for e in events)


async def test_flush_returns_even_if_the_backend_hangs(streaming_settings, executor):
    slow = FakeTranscriber("ko", seconds_per_word=0.3, latency_seconds=2.0)
    async with StreamingSession(
        session_id="s-1", transcriber=slow, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 4)
        await session.flush(timeout=0.3)
        events = await drain(session)

    assert any(isinstance(e, ServerError) and e.code == "inference_failed" for e in events)


async def test_push_audio_rejects_ragged_frames(streaming_settings, transcriber, executor):
    async with StreamingSession(
        session_id="s-1", transcriber=transcriber, settings=streaming_settings, executor=executor
    ) as session:
        with pytest.raises(AudioFormatError):
            session.push_audio(b"\x00\x00\x00")


# -- draft model -----------------------------------------------------------


async def test_draft_output_is_shown_but_never_committed(streaming_settings, executor):
    """The whole point of the draft tier: fast text the user can see, and a
    committed prefix that only ever comes from the accurate model."""
    draft = FakeTranscriber("ko", seconds_per_word=0.3, script=["빠른", "초안", "텍스트"])
    primary = FakeTranscriber("ko", seconds_per_word=0.3, script=["정확한", "최종", "텍스트"])

    async with StreamingSession(
        session_id="s-1",
        transcriber=primary,
        settings=streaming_settings,
        executor=executor,
        draft=draft,
    ) as session:
        await feed(session, [speech(0.2)] * 15)
        interim = await drain(session)
        assert interim, "no partials from the draft model"
        assert all(not e.stable for e in interim if isinstance(e, ServerTranscript)), (
            "draft text reached the client as committed"
        )
        assert any("초안" in e.partial for e in interim if isinstance(e, ServerTranscript))

        await session.flush()
        final = [e for e in await drain(session) if isinstance(e, ServerTranscript) and e.final]

    assert len(final) == 1
    assert "정확한" in final[0].stable, f"final came from the wrong model: {final[0].stable!r}"
    assert "초안" not in final[0].stable
    assert draft.calls > primary.calls, "the expensive model ran more often than the cheap one"


async def test_the_accurate_model_runs_once_per_utterance(streaming_settings, executor):
    draft = FakeTranscriber("ko", seconds_per_word=0.3)
    primary = FakeTranscriber("ko", seconds_per_word=0.3)

    async with StreamingSession(
        session_id="s-1",
        transcriber=primary,
        settings=streaming_settings,
        executor=executor,
        draft=draft,
    ) as session:
        await feed(session, [speech(0.2)] * 12)
        await session.flush()
        await drain(session)

    assert primary.calls == 1, f"the accurate model ran {primary.calls} times, want 1"
    assert session.draft_decode_count == session.decode_count - 1


async def test_a_draft_revision_does_not_split_the_utterance(streaming_settings, executor):
    """A draft model that changes its mind is normal and must not produce a
    second utterance — nothing it said was ever committed."""

    class FlipFlop(FakeTranscriber):
        def transcribe(self, pcm, *, prompt=""):
            result = super().transcribe(pcm, prompt=prompt)
            if self.calls % 2 == 0:
                return type(result)(text="완전히 다른 문장", audio_seconds=result.audio_seconds)
            return result

    async with StreamingSession(
        session_id="s-1",
        transcriber=FakeTranscriber("ko", seconds_per_word=0.3),
        settings=streaming_settings,
        executor=executor,
        draft=FlipFlop("ko", seconds_per_word=0.3),
    ) as session:
        await feed(session, [speech(0.2)] * 14)
        await session.flush()
        events = await drain(session)

    utterances = {e.utterance_id for e in events if isinstance(e, ServerTranscript)}
    assert len(utterances) == 1, f"draft churn split the utterance into {len(utterances)}"


async def test_flush_during_an_over_long_utterance_returns_promptly(executor):
    """A flush that arrives when the buffer is already over the cap must be
    served by the forced finalize, not left to time out."""
    settings = StreamingSettings(
        chunk_ms=200, silence_ms=5000, max_utterance_seconds=2, vad="energy"
    )
    slow = FakeTranscriber("ko", seconds_per_word=0.3, latency_seconds=0.2)
    async with StreamingSession(
        session_id="s-1", transcriber=slow, settings=settings, executor=executor
    ) as session:
        # No awaits between pushes: the decode loop first runs inside flush(),
        # when the buffer is already past max_utterance_seconds.
        for _ in range(3):
            session.push_audio(speech(1.0))
        await session.flush(timeout=1.0)
        events = await drain(session)

    assert any(isinstance(e, ServerError) and e.code == "utterance_too_long" for e in events)
    assert not any(
        isinstance(e, ServerError) and "flush timed out" in e.message for e in events
    ), "the flush waited out its timeout instead of being served by the forced finalize"
    assert any(e.final for e in events if isinstance(e, ServerTranscript))


async def test_draft_window_compaction_commits_accurate_text_only(executor):
    """When a draft-mode utterance outgrows the decode window, the committed
    prefix must come from an accurate pass — never from the draft."""
    settings = StreamingSettings(
        chunk_ms=200,
        silence_ms=300,
        max_utterance_seconds=30,
        max_window_seconds=1.0,
        agreement_window=2,
        vad="energy",
    )
    draft = FakeTranscriber("ko", seconds_per_word=0.3, script=["빠른", "초안", "글", "더", "더더"])
    primary = FakeTranscriber("ko", seconds_per_word=0.3, script=["정확한", "최종", "글", "더", "더더"])

    async with StreamingSession(
        session_id="s-1",
        transcriber=primary,
        settings=settings,
        executor=executor,
        draft=draft,
    ) as session:
        await feed(session, [speech(0.2)] * 14)
        await session.flush()
        events = await drain(session)

    transcripts = [e for e in events if isinstance(e, ServerTranscript)]
    committed_before_final = [e.stable for e in transcripts if not e.final and e.stable]
    assert committed_before_final, "the window never compacted, so the test proves nothing"
    assert all("초안" not in stable for stable in committed_before_final), (
        "draft text was committed at the window boundary"
    )
    assert all("정확한" in stable for stable in committed_before_final)
    assert primary.calls >= 2, "compaction should have cost an accurate pass before the final one"

    seen = ""
    for event in transcripts:
        assert event.stable.startswith(seen), "compaction retracted committed text"
        seen = event.stable


async def test_draft_finalize_failure_never_commits_the_draft_guess(streaming_settings, executor):
    """If the accurate pass fails at finalize, the plan's error rule applies:
    the draft partial is dropped, not promoted to committed text."""
    draft = FakeTranscriber("ko", seconds_per_word=0.3, script=["빠른", "초안", "글"])
    primary = FakeTranscriber("ko", seconds_per_word=0.3, fail_on_call=1)

    async with StreamingSession(
        session_id="s-1",
        transcriber=primary,
        settings=streaming_settings,
        executor=executor,
        draft=draft,
    ) as session:
        await feed(session, [speech(0.2)] * 12)
        await session.flush()
        events = await drain(session)

    assert any(isinstance(e, ServerError) and e.code == "inference_failed" for e in events)
    for event in events:
        if isinstance(event, ServerTranscript):
            assert "초안" not in event.stable, "the draft guess was committed after the failure"
            assert not event.final or event.stable == "", (
                "a final carried text no accurate pass produced"
            )


# -- re-casing is not a revision --------------------------------------------


def test_recasing_the_first_word_does_not_rotate_the_utterance():
    """Whisper lowercases the opening word once the sentence turns out to
    continue: "The dictation server" becomes "the dictation server". That is
    the same word, not a revision, and treating it as one closed the utterance
    and reopened it — so the client typed the whole sentence twice.

    Only reachable on a decoder fast enough to commit before the sentence ends,
    which is why it appeared the day the GPU backend landed and not before.
    """
    agreement = LocalAgreement(window=2)
    agreement.update("The dictation server runs on ")
    agreement.update("The dictation server runs on the laptop ")
    committed = agreement.committed
    assert committed.startswith("The dictation server")

    result = agreement.update("the dictation server runs on the laptop and the client connects.")

    assert not result.conflict, "a case change rotated the utterance"
    # What the user already has stays exactly as it was typed...
    assert result.stable.startswith("The dictation server")
    # ...and stable is still a literal prefix of what the client will type,
    # which is the invariant it refuses to compose without.
    assert (result.stable + result.partial).startswith(result.stable)
    assert "and the client connects." in result.stable + result.partial


def test_a_changed_word_is_still_a_conflict():
    """The narrowness is the point: only case is forgiven. A decoder that
    changed a word changed the text, and the caller has to rotate rather than
    quietly overwrite what is on screen."""
    agreement = LocalAgreement(window=2)
    agreement.update("the dictation server runs ")
    agreement.update("the dictation server runs on ")
    assert agreement.committed.startswith("the dictation server")

    result = agreement.update("the dictation service runs on the laptop")

    assert result.conflict


def test_commit_all_forgives_the_same_recasing():
    """The flush path has to agree with the streaming one, or an utterance that
    survived every partial pass would rotate at the very end."""
    agreement = LocalAgreement(window=2)
    agreement.update("The meeting starts ")
    agreement.update("The meeting starts at ")
    assert agreement.committed.startswith("The meeting")

    result = agreement.commit_all("the meeting starts at three o'clock.")

    assert not result.conflict
    assert result.stable.startswith("The meeting")
    assert result.stable.endswith("three o'clock.")


# -- not saying it twice ----------------------------------------------------


@pytest.mark.parametrize(
    ("previous", "hypothesis", "want"),
    [
        # The measured case: 0.18 s of audio was trimmed after "오늘" was
        # committed, and the decode of what was left opened by saying it again.
        ("오늘 ", "오늘 회의에서는 지난 분기", "회의에서는 지난 분기"),
        # A short window and a long prompt come back as the whole prompt.
        ("보고서를 제출해 주세요.", "보고서를 제출해 주세요.", ""),
        # Whisper re-spaces Korean between passes, so the two spellings of the
        # same words have to match.
        ("보고서를 제출해 주세요", "보고서를제출해주세요 그리고", "그리고"),
        # A hypothesis cut inside a multi-byte character decodes to U+FFFD.
        ("내년 상반기 보수", "�보수적으로 잡는", "적으로 잡는"),
        # Case is Whisper's to change, not a difference in the words.
        ("the quarterly report ", "The quarterly report is ready", "is ready"),
        # One shared character is a coincidence. "했습니다." and "다음" share
        # their 다, and cutting it would leave the user reading "음 주에".
        ("지난주에 끝냈습니다. ", "다음 주에 시작합니다", "다음 주에 시작합니다"),
        ("오늘 회의를 시작합니다 ", "준비해 주세요", "준비해 주세요"),
        ("", "회의를 시작합니다", "회의를 시작합니다"),
        ("오늘 ", "", ""),
    ],
)
def test_a_hypothesis_never_repeats_what_the_user_already_has(previous, hypothesis, want):
    assert drop_repeated_prefix(previous, hypothesis) == want


def test_only_what_the_decoder_was_shown_can_count_as_a_repeat():
    """A match longer than the prompt the model saw is a coincidence, and
    cutting on it would swallow text nobody has been given."""
    run = "가" * (MAX_REPEAT_CHARACTERS + 60)
    assert repeated_prefix(run, run + " 그리고") == MAX_REPEAT_CHARACTERS


# -- keeping non-speech away from the decoder -------------------------------


def test_speech_seconds_in_last_asks_only_about_the_window():
    tracker = SilenceTracker(EnergyVad(), cross_check=False)
    tracker.push(speech(1.0))
    tracker.push(silence(1.0))
    assert tracker.speech_seconds == pytest.approx(1.0, abs=0.05)
    assert tracker.speech_seconds_in_last(0.5) == pytest.approx(0.0, abs=0.03)
    assert tracker.speech_seconds_in_last(2.0) == pytest.approx(1.0, abs=0.05)
    assert tracker.speech_seconds_in_last(0.0) == 0.0


async def test_a_blip_of_noise_is_never_sent_to_the_decoder(executor):
    """One frame used to open an utterance, and the decode of the near-silence
    that followed came back as a sentence Whisper invented — "감사합니다" in
    Korean, every time. Nothing downstream can tell that apart from speech, so
    the audio must not reach the decoder at all."""
    settings = StreamingSettings(
        chunk_ms=200, silence_ms=300, max_utterance_seconds=10, vad="energy"
    )
    async with StreamingSession(
        session_id="s-1",
        transcriber=FakeTranscriber("ko", seconds_per_word=0.3),
        settings=settings,
        executor=executor,
    ) as session:
        await feed(session, [silence(0.2), speech(0.04), silence(0.2)])
        await feed(session, [silence(0.2)] * 8)
        await session.flush()
        events = await drain(session)

    assert session.decode_count == 0, "a breath was decoded, and Whisper answers those"
    assert events == []


async def test_a_short_word_is_still_worth_decoding(executor):
    """The gate above must not swallow "네". Measured with Silero, the shortest
    real Korean word registers 0.29 s of speech against 0.00 s for silence,
    room tone and breath; the default sits at 0.12 s, inside that gap."""
    settings = StreamingSettings(
        chunk_ms=200, silence_ms=300, max_utterance_seconds=10, vad="energy"
    )
    async with StreamingSession(
        session_id="s-1",
        transcriber=FakeTranscriber("ko", seconds_per_word=0.1),
        settings=settings,
        executor=executor,
    ) as session:
        await feed(session, [silence(0.2), speech(0.29)])
        await feed(session, [silence(0.2)] * 4)
        await session.flush()
        events = await drain(session)

    assert session.decode_count >= 1, "a real word was thrown away"
    assert any(e.final and e.stable.strip() for e in events if isinstance(e, ServerTranscript))


# -- what the cursor ends up with -------------------------------------------


def composed(events) -> str:
    """What the client's composer puts in the document.

    Committed text per utterance, in order, joined by the separator
    client/internal/input/composer.go uses. Assertions about repeated text
    belong here rather than on individual events, because a phrase typed twice
    is two correct-looking events whose *sequence* is the defect.
    """
    latest: dict[str, str] = {}
    order: list[str] = []
    for event in events:
        if not isinstance(event, ServerTranscript):
            continue
        if event.utterance_id not in order:
            order.append(event.utterance_id)
        latest[event.utterance_id] = event.stable
    return " ".join(latest[u].strip() for u in order if latest[u].strip())


WINDOW_SCRIPT = "오늘 오후 세 시에 회의를 시작합니다 준비해 주세요 모두".split()


class WindowTranscriber:
    """A decoder that reports the words its window holds, not its whole life.

    FakeTranscriber counts words from the audio it is handed, so it says
    *less* once the session trims — where a real decoder says the *next* words,
    picking up after the committed text it was given as a prompt. That is what
    makes trimming safe, and modelling it is the only way to exercise the
    trimmed-window path at all.

    `echo_prompt` adds Whisper's habit of carrying that prompt into its own
    output. It is not a fault injected for the test: it is measured behaviour
    of both shipping GPU backends, at every prompt length tried.
    """

    def __init__(self, *, seconds_per_word: float = 0.3, echo_prompt: bool = False) -> None:
        self._seconds_per_word = seconds_per_word
        self._echo = echo_prompt
        self.calls = 0
        self.closed = False

    name = "window"
    language = "ko"

    def warmup(self) -> None: ...

    def close(self) -> None:
        self.closed = True

    def transcribe(self, pcm, *, prompt: str = ""):
        self.calls += 1
        audio_seconds = len(pcm) / (SAMPLE_RATE * 2)
        start = len(prompt.split())
        count = min(len(WINDOW_SCRIPT) - start, int(audio_seconds / self._seconds_per_word))
        spoken = WINDOW_SCRIPT[start : start + count]
        words = [
            Word(
                text=(" " if index else "") + word,
                start=index * self._seconds_per_word,
                end=(index + 1) * self._seconds_per_word,
            )
            for index, word in enumerate(spoken)
        ]
        text = " ".join(spoken)
        if self._echo and prompt.strip():
            # The echoed words have no audio behind them, and Whisper reports
            # them at the very start of the window.
            words = [Word(text=w, start=0.0, end=0.0) for w in prompt.split()] + words
            text = " ".join(filter(None, (prompt.strip(), text)))
        return TranscriptionResult(
            text=text,
            audio_seconds=audio_seconds,
            duration_seconds=0.0,
            words=tuple(words),
        )


TRIMMING_SETTINGS = StreamingSettings(
    chunk_ms=200,
    silence_ms=300,
    max_utterance_seconds=30,
    max_window_seconds=1.0,
    agreement_window=2,
    vad="energy",
)


async def test_a_decoder_that_repeats_its_prompt_does_not_type_the_words_twice(executor):
    """The committed text goes back to the decoder as a prompt, and the decoder
    is free to say it again. The session then prepends the same committed text,
    so without a cut at the join the user gets "오늘 오늘 회의에서는"."""
    echoing = WindowTranscriber(echo_prompt=True)
    async with StreamingSession(
        session_id="s-1", transcriber=echoing, settings=TRIMMING_SETTINGS, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 20)
        await session.flush()
        events = await drain(session)

    document = composed(events)
    assert document, "nothing was transcribed, so the test proves nothing"
    assert document.split() == WINDOW_SCRIPT, f"the echo reached the cursor: {document!r}"


async def test_the_prompt_is_still_worth_carrying(executor):
    """The cut above must not cost the text itself: a decoder that behaves
    reports the same words either way."""
    plain = WindowTranscriber()
    async with StreamingSession(
        session_id="s-1", transcriber=plain, settings=TRIMMING_SETTINGS, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 20)
        await session.flush()
        plain_events = await drain(session)

    echoing = WindowTranscriber(echo_prompt=True)
    async with StreamingSession(
        session_id="s-2", transcriber=echoing, settings=TRIMMING_SETTINGS, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 20)
        await session.flush()
        echo_events = await drain(session)

    assert composed(echo_events) == composed(plain_events)


class ScriptedTranscriber:
    """Returns a staged hypothesis per call, so a revision can be staged too.

    Word timings are spread evenly over whatever audio it is given, which is
    all the streaming layer asks of them.
    """

    def __init__(self, hypotheses: list[str], *, language: str = "ko") -> None:
        self._hypotheses = hypotheses
        self.language = language
        self.calls = 0
        self.closed = False
        #: Audio each call was handed. The session trimming its window is
        #: visible here and nowhere else a test can reach.
        self.audio_seconds: list[float] = []

    name = "scripted"

    def warmup(self) -> None: ...

    def close(self) -> None:
        self.closed = True

    def transcribe(self, pcm, *, prompt: str = ""):
        text = self._hypotheses[min(self.calls, len(self._hypotheses) - 1)]
        self.calls += 1
        audio_seconds = len(pcm) / (SAMPLE_RATE * 2)
        self.audio_seconds.append(audio_seconds)
        spoken = text.split()
        span = audio_seconds / max(len(spoken), 1)
        return TranscriptionResult(
            text=text,
            audio_seconds=audio_seconds,
            duration_seconds=0.0,
            words=tuple(
                Word(text=(" " if index else "") + word, start=index * span, end=(index + 1) * span)
                for index, word in enumerate(spoken)
            ),
        )


REVISION = [
    "오늘 오후 세",
    "오늘 오후 세 시에",
    # The decoder changes its mind about a word the user already has.
    "오늘 저녁 여섯 시에 회의를",
]


async def test_a_revision_at_the_end_does_not_type_the_sentence_twice(
    streaming_settings, executor
):
    """Committed text cannot be retracted, so a contradicting reading opens a
    new utterance. Sending the *whole* reading into it typed the agreed-on
    prefix a second time — for a revision near the end of a sentence, the
    entire sentence."""
    scripted = ScriptedTranscriber(REVISION)
    async with StreamingSession(
        session_id="s-1", transcriber=scripted, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 2)
        session.push_audio(speech(0.1))
        await session.flush()
        events = await drain(session)

    document = composed(events)
    assert "오늘" in document, "nothing was committed, so the test proves nothing"
    assert document.split().count("오늘") == 1, f"the utterance was typed twice: {document!r}"
    assert document.split().count("시에") == 1, f"the tail was typed twice: {document!r}"


async def test_a_revision_mid_utterance_drops_the_audio_behind_what_was_typed(
    streaming_settings, executor
):
    """Same rule for a revision that arrives while the user is still talking,
    and one thing more: the audio behind the text they already have is still in
    the window, so unless it goes too, the very next pass reads those words and
    offers them again.

    A window that shrinks between passes is the only evidence of that a test
    can reach from outside, and nothing else in this setup can shrink one —
    max_window_seconds is ten times the audio fed here."""
    scripted = ScriptedTranscriber(REVISION + ["시에 회의를 시작합니다"])
    async with StreamingSession(
        session_id="s-1", transcriber=scripted, settings=streaming_settings, executor=executor
    ) as session:
        await feed(session, [speech(0.2)] * 6)
        await session.flush()
        events = await drain(session)

    assert len(scripted.audio_seconds) >= 4, "not enough passes to stage the revision"
    shrank = [
        (before, after)
        for before, after in zip(scripted.audio_seconds, scripted.audio_seconds[1:])
        if after < before
    ]
    assert shrank, (
        "the window never shrank, so the audio behind the typed text is still "
        f"there for the next pass to read: {scripted.audio_seconds}"
    )
    document = composed(events)
    assert document.split().count("오늘") == 1, f"the utterance was typed twice: {document!r}"


# -- decoding the speech, not the room --------------------------------------


def test_speech_span_finds_the_words_inside_the_window():
    tracker = SilenceTracker(EnergyVad(), cross_check=False)
    tracker.push(silence(1.0))
    tracker.push(speech(0.5))
    tracker.push(silence(0.8))

    span = tracker.speech_span_in_last(2.3)
    assert span is not None
    start, end = span
    assert start == pytest.approx(1.0, abs=0.05)
    assert end == pytest.approx(1.5, abs=0.05)

    padded = tracker.speech_span_in_last(2.3, pad=0.2)
    assert padded[0] == pytest.approx(0.8, abs=0.05)
    assert padded[1] == pytest.approx(1.7, abs=0.05)

    # The pad never escapes the window it is measured in.
    tight = SilenceTracker(EnergyVad(), cross_check=False)
    tight.push(speech(0.4))
    assert tight.speech_span_in_last(0.4, pad=1.0) == (0.0, pytest.approx(0.4, abs=0.05))

    quiet = SilenceTracker(EnergyVad(), cross_check=False)
    quiet.push(silence(1.0))
    assert quiet.speech_span_in_last(1.0) is None


class RecordingTranscriber(FakeTranscriber):
    """Remembers how much audio each pass was handed."""

    def __init__(self, *args, **kwargs) -> None:
        super().__init__(*args, **kwargs)
        self.audio_seconds: list[float] = []

    def transcribe(self, pcm, *, prompt: str = ""):
        self.audio_seconds.append(len(pcm) / (SAMPLE_RATE * 2))
        return super().transcribe(pcm, prompt=prompt)


PATIENT = StreamingSettings(
    chunk_ms=200, silence_ms=1500, max_utterance_seconds=30, vad="energy"
)


async def test_the_silence_around_a_word_is_not_sent_to_the_decoder(executor):
    """Silence is not neutral input. Measured on the real models: "네" on its
    own decodes as "네.", and the same clip with two seconds of silence either
    side decodes as "例". So the window is cut back to what was said, with a
    pad — and the decoder sees a fraction of the audio the buffer holds."""
    recorder = RecordingTranscriber("ko", seconds_per_word=0.2)
    async with StreamingSession(
        session_id="s-1", transcriber=recorder, settings=PATIENT, executor=executor
    ) as session:
        await feed(session, [speech(0.4)])
        await feed(session, [silence(0.3)] * 6, pause=0.03)
        await session.flush()
        await drain(session)

    assert recorder.audio_seconds, "nothing was decoded, so the test proves nothing"
    longest = max(recorder.audio_seconds)
    assert longest < 1.2, (
        f"the decoder was handed {longest:.2f}s for 0.4s of speech: {recorder.audio_seconds}"
    )


async def test_the_trim_can_be_turned_off(executor):
    recorder = RecordingTranscriber("ko", seconds_per_word=0.2)
    settings = StreamingSettings(
        chunk_ms=200, silence_ms=1500, max_utterance_seconds=30, vad="energy",
        trim_to_speech=False,
    )
    async with StreamingSession(
        session_id="s-1", transcriber=recorder, settings=settings, executor=executor
    ) as session:
        await feed(session, [speech(0.4)])
        await feed(session, [silence(0.3)] * 6, pause=0.03)
        await session.flush()
        await drain(session)

    assert max(recorder.audio_seconds) > 1.2, (
        f"trim_to_speech: false still trimmed: {recorder.audio_seconds}"
    )


async def test_trimming_does_not_change_the_text(executor):
    """The claim this is worth doing rests on it costing nothing when the audio
    was fine already."""
    async def transcript(trim: bool) -> str:
        settings = StreamingSettings(
            chunk_ms=200, silence_ms=300, max_utterance_seconds=30, vad="energy",
            trim_to_speech=trim,
        )
        async with StreamingSession(
            session_id="s-1",
            transcriber=FakeTranscriber("ko", seconds_per_word=0.3),
            settings=settings,
            executor=executor,
        ) as session:
            await feed(session, [silence(0.2)] * 3)
            await feed(session, [speech(0.2)] * 12)
            await session.flush()
            return composed(await drain(session))

    assert await transcript(True) == await transcript(False) != ""
