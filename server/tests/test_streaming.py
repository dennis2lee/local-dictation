"""Buffer, VAD, LocalAgreement and the session that ties them together."""

from __future__ import annotations

import asyncio

import pytest

from app.inference.fake import FakeTranscriber
from app.protocol import ServerError, ServerTranscript
from app.settings import StreamingSettings
from app.streaming.buffer import AudioBuffer, AudioFormatError
from app.streaming.local_agreement import LocalAgreement, tokenize
from app.streaming.session import StreamingSession
from app.streaming.vad import EnergyVad, SilenceTracker
from tests.conftest import silence, speech

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
