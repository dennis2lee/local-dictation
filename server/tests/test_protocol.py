"""Wire-format tests, including validation against the shipped JSON Schemas.

The schemas in protocol/v1/schema are the contract the Go client is written
against. If this file passes, the two implementations agree.
"""

from __future__ import annotations

import json

import pytest
from jsonschema import Draft202012Validator

from app.protocol import (
    ClientFlush,
    ClientStart,
    ClientStop,
    ProtocolError,
    ServerClosed,
    ServerError,
    ServerReady,
    ServerTranscript,
    parse_client_message,
)
from app.protocol.events import FATAL_CODES

VALID_START = {
    "type": "start",
    "protocol_version": 1,
    "session_id": "s-01J6ZH8Q4T7N2WQ4C4Q0M2Y6ZB",
    "client_version": "0.1.0",
    "language": "ko",
    "audio": {"encoding": "pcm_s16le", "sample_rate": 16000, "channels": 1},
}


def validator(schema_dir, name):
    schema = json.loads((schema_dir / name).read_text(encoding="utf-8"))
    Draft202012Validator.check_schema(schema)
    return Draft202012Validator(schema)


# -- parsing ---------------------------------------------------------------


def test_start_round_trips():
    parsed = parse_client_message(json.dumps(VALID_START))
    assert isinstance(parsed, ClientStart)
    assert parsed.session_id == VALID_START["session_id"]
    assert parsed.language == "ko"


def test_flush_and_stop():
    assert isinstance(parse_client_message('{"type": "flush"}'), ClientFlush)
    assert isinstance(parse_client_message('{"type": "stop"}'), ClientStop)


@pytest.mark.parametrize(
    ("mutation", "code"),
    [
        ({"protocol_version": 2}, "protocol_unsupported"),
        ({"language": "fr"}, "malformed_message"),
        ({"session_id": ""}, "malformed_message"),
        ({"session_id": "x" * 65}, "malformed_message"),
        ({"audio": {"encoding": "opus", "sample_rate": 16000, "channels": 1}}, "audio_format_invalid"),
        ({"audio": {"encoding": "pcm_s16le", "sample_rate": 48000, "channels": 1}}, "audio_format_invalid"),
        ({"audio": {"encoding": "pcm_s16le", "sample_rate": 16000, "channels": 2}}, "audio_format_invalid"),
        ({"unexpected": 1}, "malformed_message"),
    ],
)
def test_bad_start_maps_to_the_right_code(mutation, code):
    payload = {**VALID_START, **mutation}
    with pytest.raises(ProtocolError) as caught:
        parse_client_message(json.dumps(payload))
    assert caught.value.code == code


@pytest.mark.parametrize("raw", ["", "not json", "[]", '"text"', "{}", '{"type": "unknown"}'])
def test_junk_is_malformed(raw):
    with pytest.raises(ProtocolError) as caught:
        parse_client_message(raw)
    assert caught.value.code == "malformed_message"


def test_every_error_code_declares_whether_it_is_fatal(schema_dir):
    schema = json.loads((schema_dir / "server-error.json").read_text(encoding="utf-8"))
    declared = set(schema["properties"]["code"]["enum"])
    non_fatal = {"utterance_too_long", "inference_failed"}
    assert FATAL_CODES | non_fatal == declared, "a code exists that nobody decided about"
    assert not FATAL_CODES & non_fatal


# -- schema conformance ----------------------------------------------------


def test_start_matches_schema(schema_dir):
    validator(schema_dir, "client-start.json").validate(VALID_START)


def test_ready_matches_schema(schema_dir):
    event = ServerReady("s-1", "ko", "large-v3", "0.1.0").to_dict()
    validator(schema_dir, "server-ready.json").validate(event)


@pytest.mark.parametrize(
    "event",
    [
        ServerTranscript("u-1", 1, "", "오늘", False),
        ServerTranscript("u-1", 2, "오늘 ", "오후", False),
        ServerTranscript("u-1", 3, "오늘 오후", "", True),
    ],
)
def test_transcript_matches_schema(schema_dir, event):
    validator(schema_dir, "server-transcript.json").validate(event.to_dict())


@pytest.mark.parametrize("code", sorted(FATAL_CODES | {"inference_failed", "utterance_too_long"}))
def test_error_matches_schema(schema_dir, code):
    event = ServerError(code, "because").to_dict()
    validator(schema_dir, "server-error.json").validate(event)
    assert event["fatal"] is (code in FATAL_CODES)


@pytest.mark.parametrize("reason", ["client_stop", "server_shutdown", "idle_timeout", "error"])
def test_closed_matches_schema(schema_dir, reason):
    validator(schema_dir, "server-closed.json").validate(ServerClosed(reason).to_dict())


def test_stable_plus_partial_reconstructs_the_hypothesis():
    event = ServerTranscript("u-1", 7, "오늘 오후 ", "세 시에", False)
    assert event.text == "오늘 오후 세 시에"
