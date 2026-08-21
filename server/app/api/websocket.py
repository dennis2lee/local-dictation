"""The dictation WebSocket endpoint.

One connection is one session. Three concerns are kept apart on purpose:

* the **reader** drains the socket and never blocks on inference;
* the **writer** drains the session's event queue into the socket;
* the **session** owns decoding and the transcript invariants.

If any of the three stops, the other two are torn down with it, so a half-open
session cannot linger holding a capacity slot.
"""

from __future__ import annotations

import asyncio
import logging

from fastapi import APIRouter, WebSocket, WebSocketDisconnect
from starlette.websockets import WebSocketState

from app import __version__
from app.api.state import AppState
from app.protocol import (
    ClientFlush,
    ClientStart,
    ClientStop,
    ProtocolError,
    ServerClosed,
    ServerError,
    ServerMessage,
    ServerReady,
    parse_client_message,
)
from app.streaming.buffer import AudioFormatError
from app.streaming.session import StreamingSession

log = logging.getLogger(__name__)

router = APIRouter()

#: WebSocket close codes, chosen so a proxy log distinguishes "client is wrong"
#: from "server is full" without parsing the payload.
CLOSE_NORMAL = 1000
CLOSE_POLICY_VIOLATION = 1008
CLOSE_INTERNAL_ERROR = 1011
CLOSE_TRY_AGAIN_LATER = 1013

_CLOSE_CODE_BY_ERROR = {
    "protocol_unsupported": CLOSE_POLICY_VIOLATION,
    "language_mismatch": CLOSE_POLICY_VIOLATION,
    "malformed_message": CLOSE_POLICY_VIOLATION,
    "audio_format_invalid": CLOSE_POLICY_VIOLATION,
    "audio_before_start": CLOSE_POLICY_VIOLATION,
    "server_busy": CLOSE_TRY_AGAIN_LATER,
    "internal_error": CLOSE_INTERNAL_ERROR,
}


class _SessionClosed(Exception):
    """Internal signal: the reader finished normally."""


@router.websocket("/v1/dictation")
async def dictation(websocket: WebSocket) -> None:
    state: AppState = websocket.app.state.dictation
    await websocket.accept()

    try:
        start = await _handshake(websocket, state)
    except _Rejected:
        return

    slot = state.limiter.try_acquire()
    if slot is None:
        state.metrics.session_rejected()
        log.warning(
            "session rejected: at capacity",
            extra={"session_id": start.session_id, "max_sessions": state.limiter.max_sessions},
        )
        await _reject(
            websocket,
            state,
            ServerError(
                "server_busy",
                f"server is at capacity ({state.limiter.max_sessions} concurrent sessions)",
            ),
        )
        return

    state.metrics.session_started()
    log.info(
        "session started",
        extra={
            "session_id": start.session_id,
            "language": start.language,
            "client_version": start.client_version,
        },
    )

    session = StreamingSession(
        session_id=start.session_id,
        transcriber=state.transcriber,
        settings=state.settings.streaming,
        executor=state.executor,
        metrics=state.metrics,
    )

    reason = "error"
    try:
        async with session:
            await _send(
                websocket,
                ServerReady(
                    session_id=start.session_id,
                    language=state.settings.language,
                    model=state.transcriber.name,
                    server_version=__version__,
                ),
            )
            reason = await _pump(websocket, state, session, start.session_id)
    except WebSocketDisconnect:
        reason = "client_stop"
    except Exception:  # noqa: BLE001 - never leak a traceback into the socket
        log.exception("session failed", extra={"session_id": start.session_id})
        await _fail(websocket, ServerError("internal_error", "session failed"))
    finally:
        slot.release()
        state.metrics.session_finished()
        log.info("session ended", extra={"session_id": start.session_id, "reason": reason})

    if _is_open(websocket):
        await _send(websocket, ServerClosed(reason))  # type: ignore[arg-type]
        await websocket.close(CLOSE_NORMAL)


# --------------------------------------------------------------------------
# Handshake
# --------------------------------------------------------------------------


class _Rejected(Exception):
    """The connection was refused during the handshake and is already closed."""


async def _reject(websocket: WebSocket, state: AppState, error: ServerError) -> _Rejected:
    """Count, report and close. Returns the exception for the caller to raise."""
    state.metrics.count_error(error.code)
    await _fail(websocket, error)
    return _Rejected()


async def _handshake(websocket: WebSocket, state: AppState) -> ClientStart:
    timeout = state.settings.limits.handshake_timeout_seconds
    try:
        message = await asyncio.wait_for(websocket.receive(), timeout)
    except TimeoutError:
        raise await _reject(
            websocket, state, ServerError("session_timeout", f"no start message within {timeout}s")
        ) from None
    except WebSocketDisconnect:
        raise _Rejected from None

    if message["type"] == "websocket.disconnect":
        raise _Rejected

    if message.get("bytes") is not None:
        raise await _reject(
            websocket, state, ServerError("audio_before_start", "audio arrived before the start message")
        )

    try:
        parsed = parse_client_message(message.get("text") or "")
    except ProtocolError as exc:
        raise await _reject(websocket, state, ServerError.from_protocol_error(exc)) from None

    if not isinstance(parsed, ClientStart):
        raise await _reject(
            websocket, state, ServerError("malformed_message", "the first message must be of type 'start'")
        )

    if parsed.language != state.settings.language:
        # A misrouted client would otherwise get fluent nonsense: Whisper will
        # happily transcribe Korean audio into English words.
        raise await _reject(
            websocket,
            state,
            ServerError(
                "language_mismatch",
                f"this instance serves {state.settings.language!r}, "
                f"the client asked for {parsed.language!r}; connect to the other port",
            ),
        )

    if not state.ready:
        raise await _reject(websocket, state, ServerError("internal_error", "model is still loading"))

    return parsed


# --------------------------------------------------------------------------
# Reader / writer
# --------------------------------------------------------------------------


async def _pump(
    websocket: WebSocket, state: AppState, session: StreamingSession, session_id: str
) -> str:
    writer = asyncio.create_task(_writer(websocket, session), name=f"writer-{session_id}")
    try:
        return await _reader(websocket, state, session, session_id)
    finally:
        # Give the writer a moment to flush the final transcript before tearing
        # the socket down; the client is waiting for exactly that event.
        try:
            await asyncio.wait_for(_drain(session), timeout=2.0)
        except TimeoutError:
            log.warning("event queue did not drain", extra={"session_id": session_id})
        writer.cancel()
        try:
            await writer
        except asyncio.CancelledError:
            pass


async def _drain(session: StreamingSession) -> None:
    await session.events.join()


async def _writer(websocket: WebSocket, session: StreamingSession) -> None:
    while True:
        event = await session.events.get()
        try:
            await _send(websocket, event)
        finally:
            session.events.task_done()


async def _reader(
    websocket: WebSocket, state: AppState, session: StreamingSession, session_id: str
) -> str:
    limits = state.settings.limits
    idle_timeout = limits.idle_timeout_seconds
    max_frame = limits.max_audio_frame_bytes

    while True:
        try:
            message = await asyncio.wait_for(websocket.receive(), idle_timeout)
        except TimeoutError:
            state.metrics.count_error("session_timeout")
            await _fail(
                websocket,
                ServerError("session_timeout", f"no audio for {idle_timeout}s"),
            )
            return "idle_timeout"
        except WebSocketDisconnect:
            return "client_stop"

        if message["type"] == "websocket.disconnect":
            return "client_stop"

        payload = message.get("bytes")
        if payload is not None:
            if len(payload) > max_frame:
                state.metrics.count_error("audio_format_invalid")
                await _fail(
                    websocket,
                    ServerError(
                        "audio_format_invalid",
                        f"audio frame of {len(payload)} bytes exceeds the {max_frame} byte limit",
                    ),
                )
                return "error"
            try:
                session.push_audio(payload)
            except AudioFormatError as exc:
                state.metrics.count_error("audio_format_invalid")
                await _fail(websocket, ServerError("audio_format_invalid", str(exc)))
                return "error"
            continue

        text = message.get("text")
        if text is None:
            continue

        try:
            command = parse_client_message(text)
        except ProtocolError as exc:
            state.metrics.count_error(exc.code)
            await _fail(websocket, ServerError.from_protocol_error(exc))
            return "error"

        if isinstance(command, ClientFlush):
            await session.flush()
            continue
        if isinstance(command, ClientStop):
            await session.flush()
            return "client_stop"
        if isinstance(command, ClientStart):
            state.metrics.count_error("malformed_message")
            await _fail(
                websocket,
                ServerError("malformed_message", "a session cannot be started twice"),
            )
            return "error"


# --------------------------------------------------------------------------
# Sending
# --------------------------------------------------------------------------


def _is_open(websocket: WebSocket) -> bool:
    """True while this side may still send.

    `application_state` is what *we* have sent; `client_state` only flips once
    the peer's disconnect has been read. Gating on the latter alone lets a send
    slip through after `close()`, which Starlette turns into a RuntimeError in
    the middle of the handler.
    """
    return (
        websocket.application_state is WebSocketState.CONNECTED
        and websocket.client_state is WebSocketState.CONNECTED
    )


async def _send(websocket: WebSocket, event: ServerMessage) -> None:
    if not _is_open(websocket):
        return
    try:
        await websocket.send_json(event.to_dict())
    except (WebSocketDisconnect, RuntimeError):
        # The peer vanished mid-write. The reader will notice and unwind.
        return


async def _fail(websocket: WebSocket, error: ServerError) -> None:
    await _send(websocket, error)
    if error.fatal and _is_open(websocket):
        await websocket.close(_CLOSE_CODE_BY_ERROR.get(error.code, CLOSE_NORMAL))

