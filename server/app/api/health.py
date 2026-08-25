"""Liveness, readiness and metrics.

Kept free of any session state so a health probe can never be blocked behind a
decode.
"""

from __future__ import annotations

from fastapi import APIRouter, Request, Response
from fastapi.responses import JSONResponse, PlainTextResponse

from app import PROTOCOL_VERSION, __version__
from app.api.state import AppState

router = APIRouter()


def _state(request: Request) -> AppState:
    return request.app.state.dictation


def _engine(state: AppState) -> dict[str, str]:
    """What is actually doing the decoding.

    Read off the transcriber as optional attributes rather than widened into
    the Transcriber contract: only the accelerator backends have a device worth
    naming, and the CPU one would have to invent an answer. What matters is
    that a client which asked for a GPU can tell from outside the process
    whether it got one — OpenVINO will run a GPU-targeted model on the CPU
    given the chance, and the only symptom is that dictation is slow.
    """
    engine: dict[str, str] = {"backend": state.backend}
    device = getattr(state.transcriber, "device", None)
    if device:
        engine["device"] = str(device)
    name = getattr(state.transcriber, "device_name", None)
    if name and str(name) != str(device):
        engine["device_name"] = str(name)
    return engine


@router.get("/health")
async def health(request: Request) -> JSONResponse:
    """Liveness. 200 as long as the process is serving."""
    state = _state(request)
    return JSONResponse(
        {
            "status": "shutting_down" if state.shutting_down else "ok",
            "language": state.settings.language,
            "version": __version__,
            "protocol_version": PROTOCOL_VERSION,
        }
    )


@router.get("/health/ready")
async def ready(request: Request) -> JSONResponse:
    """Readiness. 503 until the model is loaded and warmed up."""
    state = _state(request)
    body = {
        "status": "ready" if state.ready and not state.shutting_down else "not_ready",
        "language": state.settings.language,
        "model": state.transcriber.name,
        "draft_model": state.draft.name if state.draft else None,
        "engine": _engine(state),
        "version": __version__,
        "protocol_version": PROTOCOL_VERSION,
        "sessions": {
            "active": state.limiter.active,
            "max": state.limiter.max_sessions,
        },
    }
    return JSONResponse(body, status_code=200 if body["status"] == "ready" else 503)


@router.get("/metrics")
async def metrics(request: Request) -> Response:
    return PlainTextResponse(
        _state(request).metrics.render(),
        media_type="text/plain; version=0.0.4; charset=utf-8",
    )


@router.get("/status")
async def status(request: Request) -> JSONResponse:
    """Compact JSON snapshot, for the `local-dictation-server status` command."""
    state = _state(request)
    settings = state.settings
    return JSONResponse(
        {
            "language": settings.language,
            "model": state.transcriber.name,
            "draft_model": state.draft.name if state.draft else None,
            "engine": _engine(state),
            "version": __version__,
            "protocol_version": PROTOCOL_VERSION,
            "ready": state.ready,
            "instance": settings.server.instance_name,
            "listen": f"{settings.server.host}:{settings.server.port}",
            "tls": bool(settings.security.tls_certificate),
            "mtls": settings.security.require_client_certificate,
            "retention": {
                "store_audio": settings.logging.store_audio,
                "store_transcript": settings.logging.store_transcript,
            },
            "metrics": state.metrics.summary(),
        }
    )
