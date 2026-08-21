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
