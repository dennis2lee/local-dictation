"""Speech-to-text backends.

The streaming layer only ever sees the `Transcriber` protocol, so tests run
against `FakeTranscriber` and production runs against `FasterWhisperTranscriber`
without either knowing about the other.
"""

from app.inference.base import Transcriber, TranscriptionResult, InferenceError
from app.inference.factory import create_transcriber

__all__ = ["Transcriber", "TranscriptionResult", "InferenceError", "create_transcriber"]
