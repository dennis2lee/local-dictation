"""Application factory and process entry point.

A process serves exactly one language. There is no multi-language mode and no
runtime language switch: the two instances are independent units of failure,
independently restartable, and independently visible in metrics.
"""

from __future__ import annotations

import argparse
import logging
import sys
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI

from app import PROTOCOL_VERSION, __version__
from app.api import AppState, health_router, websocket_router
from app.inference.base import InferenceError
from app.inference.factory import create_draft_transcriber, create_transcriber
from app.observability.logging import configure_logging
from app.settings import ConfigError, Settings, load_settings

log = logging.getLogger(__name__)


def create_app(settings: Settings, *, backend: str = "whisper") -> FastAPI:
    transcriber = create_transcriber(settings.model, backend=backend)
    draft = create_draft_transcriber(settings.model, backend=backend)
    state = AppState.create(settings, transcriber, draft)

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        log.info(
            "starting",
            extra={
                "language": settings.language,
                "port": settings.server.port,
                "model": transcriber.name,
                "draft_model": draft.name if draft else None,
                "max_sessions": settings.limits.max_sessions,
                "protocol_version": PROTOCOL_VERSION,
                "version": __version__,
            },
        )
        # A cold large-v3 spends its first decode loading weights; do it before
        # readiness flips so the first user does not pay for it.
        state.warmup()
        log.info("ready", extra={"language": settings.language, "port": settings.server.port})
        try:
            yield
        finally:
            log.info("stopping", extra={"language": settings.language})
            state.shutdown()

    app = FastAPI(
        title="Local Dictation Server",
        version=__version__,
        summary=f"Offline {settings.language} dictation over WebSocket",
        lifespan=lifespan,
        # No docs endpoints: this is an appliance on a closed network, and the
        # smaller the HTTP surface the easier the firewall rule.
        docs_url=None,
        redoc_url=None,
        openapi_url=None,
    )
    app.state.dictation = state
    app.include_router(health_router)
    app.include_router(websocket_router)
    return app


def preflight(settings: Settings, *, backend: str) -> tuple[list[str], list[str]]:
    """Check what a config file can only claim: that the files it names exist.

    `Settings.validate` cannot do this. It runs on every startup and throughout
    the tests, where the paths are fixtures rather than installed files. But an
    operator running `--check` before a restart is asking exactly this question,
    and a check that answers "ok" for a model directory that is not there is
    worse than no check at all.

    Returns (problems, warnings). A problem stops the server from serving; a
    warning is something it recovers from but an operator should know about.
    """
    problems: list[str] = []
    warnings: list[str] = []

    if backend != "fake":
        model = Path(settings.model.path)
        if not model.is_dir():
            problems.append(f"model.path is not a directory: {model}")
        elif not (model / "model.bin").is_file():
            problems.append(f"model.path holds no model.bin: {model}")

        if settings.model.draft_path:
            draft = Path(settings.model.draft_path)
            if not (draft / "model.bin").is_file():
                problems.append(f"model.draft_path holds no model.bin: {draft}")

    for label, path in (
        ("security.tls_certificate", settings.security.tls_certificate),
        ("security.tls_private_key", settings.security.tls_private_key),
        ("security.client_ca", settings.security.client_ca),
    ):
        if path and not Path(path).is_file():
            problems.append(f"{label} is not readable: {path}")

    # Not a problem: the server logs this and falls back to the energy detector
    # rather than refusing to serve. Still worth saying out loud.
    if settings.streaming.vad == "silero" and settings.streaming.silero_model_path:
        silero = Path(settings.streaming.silero_model_path)
        if not silero.is_file():
            warnings.append(
                f"streaming.silero_model_path is not readable: {silero} "
                "— the server will fall back to the energy detector"
            )

    return problems, warnings


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="local-dictation-serve",
        description="Run one Local Dictation language server.",
    )
    parser.add_argument("--config", help="path to a YAML config file")
    parser.add_argument("--host", help="override server.host")
    parser.add_argument("--port", type=int, help="override server.port")
    parser.add_argument("--language", choices=["ko", "en"], help="override model.language")
    parser.add_argument(
        "--backend",
        choices=["whisper", "fake"],
        default="whisper",
        help="inference backend; 'fake' emits scripted text and loads no model",
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="validate the configuration and exit without binding a port",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    return parser


def _resolve(args: argparse.Namespace) -> Settings:
    overrides: dict[str, str] = {}
    if args.host:
        overrides["LOCAL_DICTATION_SERVER__HOST"] = args.host
    if args.port:
        overrides["LOCAL_DICTATION_SERVER__PORT"] = str(args.port)
    if args.language:
        overrides["LOCAL_DICTATION_MODEL__LANGUAGE"] = args.language

    import os

    environ = dict(os.environ)
    environ.update(overrides)
    return load_settings(args.config, environ=environ)


def cli(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)

    try:
        settings = _resolve(args)
    except ConfigError as exc:
        print(f"configuration error: {exc}", file=sys.stderr)
        return 2

    configure_logging(settings.logging.level, as_json=settings.logging.json)

    if settings.logging.store_audio or settings.logging.store_transcript:
        # Loud on purpose: the default is off, and turning it on is a decision
        # someone has to be able to find in the journal afterwards.
        log.warning(
            "content retention is ENABLED",
            extra={
                "store_audio": settings.logging.store_audio,
                "store_transcript": settings.logging.store_transcript,
            },
        )

    if args.check:
        problems, warnings = preflight(settings, backend=args.backend)
        where = f"{settings.language} on {settings.server.host}:{settings.server.port}"
        for warning in warnings:
            print(f"warning: {warning}", file=sys.stderr)
        if problems:
            print(f"FAILED: {where} would not serve:", file=sys.stderr)
            for problem in problems:
                print(f"  - {problem}", file=sys.stderr)
            return 1
        print(
            f"ok: {where}, model={settings.model.path}, "
            f"max_sessions={settings.limits.max_sessions}"
        )
        return 0

    try:
        app = create_app(settings, backend=args.backend)
    except InferenceError as exc:
        print(f"failed to load the model: {exc}", file=sys.stderr)
        return 3

    import uvicorn

    security = settings.security
    uvicorn.run(
        app,
        host=settings.server.host,
        port=settings.server.port,
        log_config=None,  # configure_logging already owns the handlers
        ssl_certfile=security.tls_certificate,
        ssl_keyfile=security.tls_private_key,
        ssl_ca_certs=security.client_ca,
        ssl_cert_reqs=2 if security.require_client_certificate else 0,  # ssl.CERT_REQUIRED / CERT_NONE
        ws_max_size=settings.limits.max_audio_frame_bytes * 2,
        timeout_graceful_shutdown=10,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(cli())
