from __future__ import annotations

import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest

SERVER_ROOT = Path(__file__).resolve().parents[1]
REPO_ROOT = SERVER_ROOT.parent
sys.path.insert(0, str(SERVER_ROOT))

SAMPLE_RATE = 16000

from app.inference.fake import FakeTranscriber  # noqa: E402
from app.settings import LimitSettings, Settings, StreamingSettings  # noqa: E402


def speech(seconds: float) -> bytes:
    """PCM the energy detector classifies as speech."""
    return b"\x40\x10" * int(SAMPLE_RATE * seconds)


def silence(seconds: float) -> bytes:
    return b"\x00\x00" * int(SAMPLE_RATE * seconds)


@pytest.fixture
def schema_dir() -> Path:
    return REPO_ROOT / "protocol" / "v1" / "schema"


@pytest.fixture
def streaming_settings() -> StreamingSettings:
    # Short windows so tests finish in milliseconds rather than seconds.
    return StreamingSettings(
        chunk_ms=200,
        silence_ms=300,
        max_utterance_seconds=5,
        agreement_window=2,
        vad="energy",
    )


@pytest.fixture
def transcriber() -> FakeTranscriber:
    return FakeTranscriber("ko", seconds_per_word=0.3)


@pytest.fixture
def executor():
    with ThreadPoolExecutor(max_workers=2) as pool:
        yield pool


@pytest.fixture
def settings(streaming_settings: StreamingSettings) -> Settings:
    return Settings(
        streaming=streaming_settings,
        limits=LimitSettings(
            max_sessions=1,
            max_audio_frame_bytes=65536,
            idle_timeout_seconds=5,
            handshake_timeout_seconds=2,
        ),
    )
