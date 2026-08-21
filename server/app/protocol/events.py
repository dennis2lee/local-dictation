"""Typed client and server events, plus a strict parser for inbound JSON.

Parsing is deliberately unforgiving: an unknown key or a wrong constant is a
`malformed_message`, not something to shrug off. A dictation client that sends
an unexpected shape is a client that will mis-render committed text.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Literal

from app import PROTOCOL_VERSION

Language = Literal["ko", "en"]

ErrorCode = Literal[
    "protocol_unsupported",
    "language_mismatch",
    "server_busy",
    "audio_format_invalid",
    "audio_before_start",
    "utterance_too_long",
    "inference_failed",
    "session_timeout",
    "malformed_message",
    "internal_error",
]

CloseReason = Literal["client_stop", "server_shutdown", "idle_timeout", "error"]

#: Errors after which the socket is closed. Kept next to the codes so a new
#: code cannot be added without deciding whether it ends the session.
FATAL_CODES: frozenset[str] = frozenset(
    {
        "protocol_unsupported",
        "language_mismatch",
        "server_busy",
        "audio_format_invalid",
        "audio_before_start",
        "session_timeout",
        "malformed_message",
        "internal_error",
    }
)

REQUIRED_AUDIO = {"encoding": "pcm_s16le", "sample_rate": 16000, "channels": 1}
SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2


class ProtocolError(Exception):
    """A client message that cannot be honoured. Carries the wire error code."""

    def __init__(self, code: ErrorCode, message: str) -> None:
        super().__init__(message)
        self.code: ErrorCode = code
        self.message = message

    @property
    def fatal(self) -> bool:
        return self.code in FATAL_CODES


# --------------------------------------------------------------------------
# Client to server
# --------------------------------------------------------------------------


@dataclass(frozen=True)
class ClientStart:
    session_id: str
    language: Language
    client_version: str = ""
    protocol_version: int = PROTOCOL_VERSION


@dataclass(frozen=True)
class ClientFlush:
    pass


@dataclass(frozen=True)
class ClientStop:
    pass


ClientMessage = ClientStart | ClientFlush | ClientStop


def _require_mapping(raw: str) -> dict[str, Any]:
    try:
        payload = json.loads(raw)
    except (ValueError, TypeError) as exc:
        raise ProtocolError("malformed_message", f"not valid JSON: {exc}") from exc
    if not isinstance(payload, dict):
        raise ProtocolError("malformed_message", "message must be a JSON object")
    return payload


def _check_version(payload: dict[str, Any]) -> None:
    version = payload.get("protocol_version", PROTOCOL_VERSION)
    if version != PROTOCOL_VERSION:
        raise ProtocolError(
            "protocol_unsupported",
            f"server implements protocol version {PROTOCOL_VERSION}, client asked for {version!r}",
        )


def _reject_unknown(payload: dict[str, Any], allowed: set[str], kind: str) -> None:
    unknown = set(payload) - allowed
    if unknown:
        raise ProtocolError(
            "malformed_message", f"unknown field(s) in {kind}: {', '.join(sorted(unknown))}"
        )


def parse_client_message(raw: str) -> ClientMessage:
    """Parse one inbound text frame. Raises ProtocolError with a wire code."""
    payload = _require_mapping(raw)
    kind = payload.get("type")

    if kind == "start":
        _reject_unknown(
            payload,
            {"type", "protocol_version", "session_id", "client_version", "language", "audio"},
            "start",
        )
        _check_version(payload)

        session_id = payload.get("session_id")
        if not isinstance(session_id, str) or not 1 <= len(session_id) <= 64:
            raise ProtocolError("malformed_message", "start.session_id must be a 1..64 char string")

        language = payload.get("language")
        if language not in ("ko", "en"):
            raise ProtocolError("malformed_message", f"start.language must be ko or en, got {language!r}")

        audio = payload.get("audio")
        if not isinstance(audio, dict):
            raise ProtocolError("audio_format_invalid", "start.audio must be an object")
        for key, expected in REQUIRED_AUDIO.items():
            if audio.get(key) != expected:
                raise ProtocolError(
                    "audio_format_invalid",
                    f"start.audio.{key} must be {expected!r}, got {audio.get(key)!r}",
                )

        client_version = payload.get("client_version", "")
        if not isinstance(client_version, str):
            raise ProtocolError("malformed_message", "start.client_version must be a string")

        return ClientStart(
            session_id=session_id,
            language=language,
            client_version=client_version,
            protocol_version=PROTOCOL_VERSION,
        )

    if kind == "flush":
        _reject_unknown(payload, {"type", "protocol_version"}, "flush")
        _check_version(payload)
        return ClientFlush()

    if kind == "stop":
        _reject_unknown(payload, {"type", "protocol_version"}, "stop")
        _check_version(payload)
        return ClientStop()

    raise ProtocolError("malformed_message", f"unknown message type: {kind!r}")


# --------------------------------------------------------------------------
# Server to client
# --------------------------------------------------------------------------


@dataclass(frozen=True)
class ServerReady:
    session_id: str
    language: Language
    model: str
    server_version: str

    def to_dict(self) -> dict[str, Any]:
        return {
            "type": "ready",
            "protocol_version": PROTOCOL_VERSION,
            "session_id": self.session_id,
            "language": self.language,
            "model": self.model,
            "server_version": self.server_version,
        }


@dataclass(frozen=True)
class ServerTranscript:
    """One rendering of the current utterance.

    ``stable + partial`` reconstructs the hypothesis exactly, so ``stable``
    keeps the separator that precedes ``partial`` (usually a trailing space).
    """

    utterance_id: str
    revision: int
    stable: str
    partial: str
    final: bool

    @property
    def text(self) -> str:
        return self.stable + self.partial

    def to_dict(self) -> dict[str, Any]:
        return {
            "type": "transcript",
            "protocol_version": PROTOCOL_VERSION,
            "utterance_id": self.utterance_id,
            "revision": self.revision,
            "stable": self.stable,
            "partial": self.partial,
            "final": self.final,
        }


@dataclass(frozen=True)
class ServerError:
    code: ErrorCode
    message: str

    @property
    def fatal(self) -> bool:
        return self.code in FATAL_CODES

    @classmethod
    def from_protocol_error(cls, exc: ProtocolError) -> ServerError:
        return cls(code=exc.code, message=exc.message)

    def to_dict(self) -> dict[str, Any]:
        return {
            "type": "error",
            "protocol_version": PROTOCOL_VERSION,
            "code": self.code,
            "message": self.message,
            "fatal": self.fatal,
        }


@dataclass(frozen=True)
class ServerClosed:
    reason: CloseReason

    def to_dict(self) -> dict[str, Any]:
        return {
            "type": "closed",
            "protocol_version": PROTOCOL_VERSION,
            "reason": self.reason,
        }


ServerMessage = ServerReady | ServerTranscript | ServerError | ServerClosed
