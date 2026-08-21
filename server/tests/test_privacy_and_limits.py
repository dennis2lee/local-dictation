"""The two promises the plan makes to users, asserted rather than assumed:
nothing is retained, and capacity is refused rather than queued.
"""

from __future__ import annotations

import json
import logging

import pytest
from starlette.testclient import TestClient

from app.main import create_app
from app.observability.logging import ContentLeak, JsonFormatter, TranscriptGuard, configure_logging
from app.observability.metrics import Metrics
from app.runtime.limits import SessionLimiter
from app.settings import Settings
from tests.conftest import speech

SECRET = "오늘 오후 세 시에 회의를 시작합니다"

START = {
    "type": "start",
    "protocol_version": 1,
    "session_id": "s-privacy",
    "language": "ko",
    "audio": {"encoding": "pcm_s16le", "sample_rate": 16000, "channels": 1},
}


# -- retention -------------------------------------------------------------


def test_a_full_session_leaves_nothing_in_the_logs(settings: Settings, caplog):
    caplog.set_level(logging.DEBUG)
    app = create_app(settings, backend="fake")
    with TestClient(app) as client:
        with client.websocket_connect("/v1/dictation") as websocket:
            websocket.send_text(json.dumps(START))
            assert websocket.receive_json()["type"] == "ready"
            for _ in range(12):
                websocket.send_bytes(speech(0.2))
            websocket.send_text(json.dumps({"type": "flush"}))
            transcripts = []
            while True:
                event = websocket.receive_json()
                transcripts.append(event)
                if event.get("final"):
                    break

    recognised = transcripts[-1]["stable"]
    assert recognised, "the test needs the session to have produced text"

    logged = "\n".join(record.getMessage() for record in caplog.records)
    formatted = "\n".join(JsonFormatter().format(record) for record in caplog.records)
    for haystack in (logged, formatted):
        assert recognised not in haystack
        for word in recognised.split():
            assert word not in haystack


def test_metrics_carry_no_content():
    metrics = Metrics(language="ko", model="large-v3")
    metrics.session_started()
    metrics.observe_first_partial(1.1)
    metrics.observe_decode(audio_seconds=2.0, duration_seconds=1.0)
    metrics.count_error("inference_failed")
    body = metrics.render()
    assert SECRET not in body
    for word in SECRET.split():
        assert word not in body


def test_the_content_guard_redacts_in_production():
    guard = TranscriptGuard(strict=False)
    record = logging.LogRecord("t", logging.INFO, __file__, 1, "decoded", (), None)
    record.transcript = SECRET
    assert guard.filter(record)
    assert record.transcript == "<redacted>"


def test_the_content_guard_raises_in_strict_mode():
    guard = TranscriptGuard(strict=True)
    record = logging.LogRecord("t", logging.INFO, __file__, 1, "decoded", (), None)
    record.stable = SECRET
    with pytest.raises(ContentLeak):
        guard.filter(record)


def test_configure_logging_installs_the_guard():
    configure_logging("INFO", as_json=True)
    handler = logging.getLogger().handlers[0]
    assert any(isinstance(f, TranscriptGuard) for f in handler.filters)


def test_retention_defaults_are_off_in_the_shipped_configs():
    from app.settings import load_settings
    from tests.conftest import SERVER_ROOT

    for name in ("server-ko.yaml", "server-en.yaml"):
        loaded = load_settings(SERVER_ROOT / "config" / name, environ={})
        assert loaded.logging.store_audio is False
        assert loaded.logging.store_transcript is False


# -- capacity --------------------------------------------------------------


def test_limiter_refuses_beyond_capacity():
    limiter = SessionLimiter(2)
    first, second = limiter.try_acquire(), limiter.try_acquire()
    assert first and second
    assert limiter.try_acquire() is None
    assert limiter.active == 2
    assert limiter.rejected_total == 1

    first.release()
    assert limiter.active == 1
    third = limiter.try_acquire()
    assert third is not None
    assert limiter.accepted_total == 3


def test_releasing_twice_is_harmless():
    limiter = SessionLimiter(1)
    slot = limiter.try_acquire()
    assert slot is not None
    slot.release()
    slot.release()
    assert limiter.active == 0


def test_limiter_is_a_context_manager():
    limiter = SessionLimiter(1)
    with limiter.try_acquire() as slot:
        assert slot is not None
        assert limiter.active == 1
    assert limiter.active == 0


def test_limiter_rejects_a_nonsense_capacity():
    with pytest.raises(ValueError):
        SessionLimiter(0)
