"""Wire format for protocol v1.

The authoritative definition lives in ``protocol/v1/`` at the repository root;
``tests/test_protocol_schema.py`` validates every event this module can build
against those JSON Schemas, so the two cannot drift.
"""

from app.protocol.events import (
    ClientFlush,
    ClientMessage,
    ClientStart,
    ClientStop,
    CloseReason,
    ErrorCode,
    ProtocolError,
    ServerClosed,
    ServerError,
    ServerMessage,
    ServerReady,
    ServerTranscript,
    parse_client_message,
)

__all__ = [
    "ClientFlush",
    "ClientMessage",
    "ClientStart",
    "ClientStop",
    "CloseReason",
    "ErrorCode",
    "ProtocolError",
    "ServerClosed",
    "ServerError",
    "ServerMessage",
    "ServerReady",
    "ServerTranscript",
    "parse_client_message",
]
