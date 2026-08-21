"""HTTP and WebSocket behaviour, driven through a real ASGI stack."""

from __future__ import annotations

import json

import pytest
from starlette.testclient import TestClient

from app.main import create_app
from app.settings import LimitSettings, LoggingSettings, ModelSettings, Settings
from tests.conftest import silence, speech

START = {
    "type": "start",
    "protocol_version": 1,
    "session_id": "s-test",
    "client_version": "0.1.0",
    "language": "ko",
    "audio": {"encoding": "pcm_s16le", "sample_rate": 16000, "channels": 1},
}


@pytest.fixture
def client(settings: Settings):
    app = create_app(settings, backend="fake")
    with TestClient(app) as test_client:
        yield test_client


def start(websocket, **overrides):
    websocket.send_text(json.dumps({**START, **overrides}))
    return websocket.receive_json()


# -- health ----------------------------------------------------------------


def test_health_is_live(client):
    body = client.get("/health").json()
    assert body["status"] == "ok"
    assert body["language"] == "ko"
    assert body["protocol_version"] == 1


def test_ready_reports_the_model(client):
    response = client.get("/health/ready")
    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "ready"
    assert body["sessions"] == {"active": 0, "max": 1}


def test_ready_is_503_before_warmup(settings):
    app = create_app(settings, backend="fake")
    # Bypass the lifespan so the model has not been warmed up.
    with TestClient(app) as test_client:
        test_client.app.state.dictation.ready = False
        assert test_client.get("/health/ready").status_code == 503


def test_metrics_are_prometheus_text(client):
    response = client.get("/metrics")
    assert response.status_code == 200
    assert "text/plain" in response.headers["content-type"]
    assert "dictation_sessions_active" in response.text


def test_status_reports_the_retention_policy(client):
    body = client.get("/status").json()
    assert body["retention"] == {"store_audio": False, "store_transcript": False}
    assert body["language"] == "ko"


def test_there_is_no_openapi_surface(client):
    for path in ("/docs", "/redoc", "/openapi.json"):
        assert client.get(path).status_code == 404


# -- handshake -------------------------------------------------------------


def test_a_session_gets_ready_then_transcripts(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        assert start(websocket)["type"] == "ready"
        for _ in range(12):
            websocket.send_bytes(speech(0.2))
        websocket.send_text(json.dumps({"type": "flush"}))

        events = []
        while True:
            event = websocket.receive_json()
            events.append(event)
            if event.get("final"):
                break

    assert all(e["type"] == "transcript" for e in events)
    revisions = [e["revision"] for e in events]
    assert all(b > a for a, b in zip(revisions, revisions[1:]))
    assert events[-1]["partial"] == ""


def test_wrong_language_is_refused(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        event = start(websocket, language="en")
    assert event["type"] == "error"
    assert event["code"] == "language_mismatch"
    assert event["fatal"] is True


def test_unknown_protocol_version_is_refused(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        event = start(websocket, protocol_version=99)
    assert event["code"] == "protocol_unsupported"


def test_audio_before_start_is_refused(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        websocket.send_bytes(speech(0.1))
        event = websocket.receive_json()
    assert event["code"] == "audio_before_start"


def test_a_second_start_is_refused(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        start(websocket)
        websocket.send_text(json.dumps(START))
        event = websocket.receive_json()
    assert event["code"] == "malformed_message"


def test_ragged_audio_frame_is_refused(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        start(websocket)
        websocket.send_bytes(b"\x00\x00\x00")
        event = websocket.receive_json()
    assert event["code"] == "audio_format_invalid"


def test_oversized_audio_frame_is_refused(settings: Settings):
    settings = Settings(
        streaming=settings.streaming,
        limits=LimitSettings(max_sessions=1, max_audio_frame_bytes=1024, idle_timeout_seconds=5),
    )
    app = create_app(settings, backend="fake")
    with TestClient(app) as test_client:
        with test_client.websocket_connect("/v1/dictation") as websocket:
            start(websocket)
            websocket.send_bytes(speech(1.0))
            event = websocket.receive_json()
    assert event["code"] == "audio_format_invalid"
    assert "exceeds" in event["message"]


def test_stop_closes_cleanly(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        start(websocket)
        websocket.send_bytes(speech(0.5))
        websocket.send_text(json.dumps({"type": "stop"}))
        events = []
        while True:
            event = websocket.receive_json()
            events.append(event)
            if event["type"] == "closed":
                break
    assert events[-1]["reason"] == "client_stop"


def test_capacity_is_a_hard_gate(client):
    with client.websocket_connect("/v1/dictation") as first:
        assert start(first)["type"] == "ready"
        with client.websocket_connect("/v1/dictation") as second:
            event = start(second)
    assert event["type"] == "error"
    assert event["code"] == "server_busy"
    assert event["fatal"] is True

    # And the slot comes back once the first session is gone.
    with client.websocket_connect("/v1/dictation") as third:
        assert start(third)["type"] == "ready"


def test_capacity_rejection_is_counted(client):
    with client.websocket_connect("/v1/dictation") as first:
        start(first)
        with client.websocket_connect("/v1/dictation") as second:
            start(second)
    body = client.get("/metrics").text
    assert 'dictation_sessions_rejected_total{language="ko",model="fake"} 1' in body
    assert 'code="server_busy"' in body


def test_silence_only_session_finalizes_without_text(client):
    with client.websocket_connect("/v1/dictation") as websocket:
        start(websocket)
        for _ in range(6):
            websocket.send_bytes(silence(0.2))
        websocket.send_text(json.dumps({"type": "stop"}))
        events = []
        while True:
            event = websocket.receive_json()
            events.append(event)
            if event["type"] == "closed":
                break
    assert [e["type"] for e in events] == ["closed"]


def test_english_instance_refuses_korean(streaming_settings):
    settings = Settings(
        model=ModelSettings(language="en"),
        streaming=streaming_settings,
        limits=LimitSettings(max_sessions=1),
        logging=LoggingSettings(level="WARNING"),
    )
    app = create_app(settings, backend="fake")
    with TestClient(app) as test_client:
        assert test_client.get("/health").json()["language"] == "en"
        with test_client.websocket_connect("/v1/dictation") as websocket:
            event = start(websocket, language="ko")
    assert event["code"] == "language_mismatch"
