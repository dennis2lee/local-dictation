"""Configuration loading.

A server instance is fully described by one YAML file. Environment variables
prefixed with ``LOCAL_DICTATION_`` override individual leaves, which is how the
systemd units inject per-host paths without editing the shipped config.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field, fields, is_dataclass
from pathlib import Path
from typing import Any, Literal, get_args, get_origin, get_type_hints

import yaml

Language = Literal["ko", "en"]

ENV_PREFIX = "LOCAL_DICTATION_"


class ConfigError(ValueError):
    """Raised when a config file is structurally wrong or self-contradictory."""


@dataclass(frozen=True)
class ServerSettings:
    host: str = "0.0.0.0"
    port: int = 8765
    # Presented to clients in the `ready` event; useful when several hosts serve
    # the same language behind a load balancer.
    instance_name: str = "local-dictation"


@dataclass(frozen=True)
class ModelSettings:
    #: Local directory holding a CTranslate2 conversion. Never a HuggingFace
    #: repo id: the server must not reach the internet.
    path: str = "/opt/local-dictation/models/large-v3-turbo"
    device: str = "cpu"
    compute_type: str = "int8"
    language: Language = "ko"
    beam_size: int = 1
    temperature: float = 0.0
    #: 0 lets CTranslate2 pick; pin it to physical cores after benchmarking.
    cpu_threads: int = 0
    num_workers: int = 1
    #: Optional small model used only for the live partial text.
    #:
    #: Whisper's encoder always processes a padded 30-second window, so a decode
    #: pass costs the same whether it covers three seconds or ten. That fixed
    #: cost — measured at roughly 3.2 s for large-v3-turbo INT8 on eight
    #: performance cores — is the floor on how quickly partial text can appear,
    #: and it is well above the plan's 2 s target. A `base` conversion runs the
    #: same pass in about 0.3 s.
    #:
    #: When this is set, partials come from the draft model and the accurate
    #: model decodes the utterance once, at the end, for the committed text.
    #: See docs/latency.md.
    draft_path: str | None = None
    #: Domain vocabulary hint. Keep it short — it is prepended to every decode.
    initial_prompt: str | None = None
    #: Set false only when an operator deliberately trades accuracy for latency.
    condition_on_previous_text: bool = False


@dataclass(frozen=True)
class StreamingSettings:
    #: How much new audio to accumulate before running another decode pass.
    chunk_ms: int = 600
    #: Trailing silence that ends an utterance.
    silence_ms: int = 600
    #: Hard cap; the utterance is force-finalized and a non-fatal error is sent.
    max_utterance_seconds: int = 120
    #: Longest stretch of audio a single decode pass may cover. Once the buffer
    #: exceeds this and there is committed text to anchor on, the already
    #: transcribed audio is dropped and its text is carried forward as a prompt.
    #: This is what keeps per-pass cost flat over a long utterance.
    max_window_seconds: float = 12.0
    #: Number of consecutive agreeing hypotheses required to commit a prefix.
    #: 2 is the LocalAgreement-2 policy; 3 is more conservative and slower.
    agreement_window: int = 2
    #: Detected speech a decode window must hold before it is worth decoding.
    #:
    #: Handed a window with no speech in it, Whisper does not return nothing.
    #: It returns the boilerplate its training subtitles ended on — "감사합니다"
    #: in Korean, "Thank you for watching" in English — and it returns it
    #: confidently: measured on this project's own models, a second of digital
    #: silence decodes to "감사합니다." with no_speech_prob 0.00 and avg_logprob
    #: -0.47, which is to say no threshold downstream can tell it apart from
    #: real speech. It has to be kept away from the decoder instead.
    #:
    #: One frame used to be enough to open an utterance, so a door closing or
    #: a breath at the end of a sentence produced a decode of essentially
    #: nothing, and that decode produced a phantom sentence.
    #:
    #: Measured with Silero on this project's test clips: the shortest real
    #: Korean word ("네") registers 0.29 s of speech, while digital silence,
    #: room tone and breath all register 0.00 s. 120 ms sits inside that gap
    #: with room on both sides. 0 restores the old behaviour of decoding
    #: whatever the detector twitched at.
    min_speech_ms: int = 120
    #: Cut the non-speech off both ends of a window before decoding it.
    #:
    #: Silence is not neutral input. Whisper reads what surrounds a word as
    #: context and answers differently for it: measured on this project's own
    #: clips, "네" on its own decodes as "네.", and the same clip with two
    #: seconds of silence either side decodes as "例". Sentences came back
    #: unchanged either way, so this costs nothing on audio that was fine
    #: already — and it decodes less audio, so it is also faster.
    #:
    #: A pad is kept on both sides; see app/streaming/session.py. Turn this off
    #: only to rule it out while diagnosing something else.
    trim_to_speech: bool = True
    vad: Literal["silero", "energy", "none"] = "silero"
    #: Only used by the energy detector. RMS below this counts as silence.
    energy_threshold: float = 0.006
    #: Local path to silero_vad.onnx. Required when vad == "silero". If the file
    #: is absent at startup the server logs a warning and falls back to "energy"
    #: rather than refusing to serve.
    silero_model_path: str | None = "/opt/local-dictation/models/silero_vad.onnx"


@dataclass(frozen=True)
class SecuritySettings:
    tls_certificate: str | None = None
    tls_private_key: str | None = None
    #: CA bundle used to verify client certificates.
    client_ca: str | None = None
    require_client_certificate: bool = False


@dataclass(frozen=True)
class LimitSettings:
    #: Concurrent dictation sessions. Beyond this the server returns
    #: `server_busy` immediately rather than queueing behind the CPU.
    max_sessions: int = 2
    max_audio_frame_bytes: int = 65536
    #: Close a session that sends no audio for this long.
    idle_timeout_seconds: int = 60
    #: Refuse a `start` that takes longer than this to arrive.
    handshake_timeout_seconds: int = 10


@dataclass(frozen=True)
class LoggingSettings:
    level: str = "INFO"
    #: Both default to false and are asserted by tests. Turning them on is an
    #: explicit, auditable choice — see docs/server-usage.md.
    store_audio: bool = False
    store_transcript: bool = False
    #: Emit one JSON object per line instead of human-readable lines.
    json: bool = True


@dataclass(frozen=True)
class Settings:
    server: ServerSettings = field(default_factory=ServerSettings)
    model: ModelSettings = field(default_factory=ModelSettings)
    streaming: StreamingSettings = field(default_factory=StreamingSettings)
    security: SecuritySettings = field(default_factory=SecuritySettings)
    limits: LimitSettings = field(default_factory=LimitSettings)
    logging: LoggingSettings = field(default_factory=LoggingSettings)

    @property
    def language(self) -> Language:
        return self.model.language

    def validate(self) -> None:
        """Fail loudly at startup rather than mid-session."""
        errors: list[str] = []

        if self.model.language not in ("ko", "en"):
            errors.append(f"model.language must be 'ko' or 'en', got {self.model.language!r}")
        if not 1 <= self.server.port <= 65535:
            errors.append(f"server.port out of range: {self.server.port}")
        if self.streaming.chunk_ms < 100:
            errors.append("streaming.chunk_ms below 100 wastes CPU on redundant decodes")
        if self.streaming.silence_ms < 100:
            errors.append("streaming.silence_ms below 100 chops utterances mid-word")
        if not 0 <= self.streaming.min_speech_ms <= 1000:
            errors.append(
                "streaming.min_speech_ms must be between 0 and 1000; the "
                "shortest real word measures about 290 ms of detected speech, "
                "so anything near that ceiling drops words the user said"
            )
        if self.streaming.agreement_window < 2:
            errors.append("streaming.agreement_window must be >= 2")
        if self.streaming.max_utterance_seconds < 5:
            errors.append("streaming.max_utterance_seconds must be >= 5")
        if self.streaming.max_window_seconds < 3:
            errors.append("streaming.max_window_seconds must be >= 3")
        if self.streaming.max_window_seconds > self.streaming.max_utterance_seconds:
            errors.append("streaming.max_window_seconds cannot exceed max_utterance_seconds")
        if self.limits.max_sessions < 1:
            errors.append("limits.max_sessions must be >= 1")
        if self.limits.max_audio_frame_bytes < 640:
            errors.append("limits.max_audio_frame_bytes must hold at least one 20 ms frame")

        if self.streaming.vad == "silero" and not self.streaming.silero_model_path:
            errors.append("streaming.silero_model_path is required when streaming.vad == 'silero'")

        # TLS is all-or-nothing; a half-configured listener is worse than a
        # plain one because operators assume it is encrypted.
        cert, key = self.security.tls_certificate, self.security.tls_private_key
        if bool(cert) != bool(key):
            errors.append("security.tls_certificate and security.tls_private_key must be set together")
        if self.security.require_client_certificate and not self.security.client_ca:
            errors.append("security.client_ca is required when require_client_certificate is true")
        if self.security.require_client_certificate and not cert:
            errors.append("client certificates require TLS to be configured")

        if errors:
            raise ConfigError("invalid configuration:\n  - " + "\n  - ".join(errors))


def _coerce(value: Any, annotation: Any) -> Any:
    """Coerce a YAML scalar to the annotated type, tolerating `X | None`."""
    origin = get_origin(annotation)
    if origin is Literal:
        return value
    if origin is not None:  # X | None, and anything else parameterised
        args = [a for a in get_args(annotation) if a is not type(None)]
        if value is None:
            return None
        annotation = args[0] if args else str
        return _coerce(value, annotation)
    if annotation is bool:
        if isinstance(value, str):
            return value.strip().lower() in ("1", "true", "yes", "on")
        return bool(value)
    if annotation in (int, float, str):
        return annotation(value)
    return value


def _build(cls: type, data: dict[str, Any], path: str) -> Any:
    if not isinstance(data, dict):
        raise ConfigError(f"{path or 'config'} must be a mapping, got {type(data).__name__}")
    # `from __future__ import annotations` makes field.type a string, so resolve
    # the real annotations before coercing anything.
    hints = get_type_hints(cls)
    known = {f.name for f in fields(cls)}
    unknown = set(data) - known
    if unknown:
        # Silently ignoring a typo'd key is how a server ends up storing audio
        # because someone wrote `store_audios: false`.
        raise ConfigError(f"unknown key(s) in {path or 'config'}: {', '.join(sorted(unknown))}")

    kwargs: dict[str, Any] = {}
    for name in known:
        if name not in data:
            continue
        annotation = hints[name]
        child_path = f"{path}.{name}" if path else name
        if isinstance(annotation, type) and is_dataclass(annotation):
            kwargs[name] = _build(annotation, data[name], child_path)
            continue
        try:
            kwargs[name] = _coerce(data[name], annotation)
        except (TypeError, ValueError) as exc:
            raise ConfigError(f"{child_path}: {exc}") from exc
    return cls(**kwargs)


_SECTIONS: dict[str, type] = {
    "server": ServerSettings,
    "model": ModelSettings,
    "streaming": StreamingSettings,
    "security": SecuritySettings,
    "limits": LimitSettings,
    "logging": LoggingSettings,
}


def _apply_env(data: dict[str, Any], environ: dict[str, str]) -> dict[str, Any]:
    """LOCAL_DICTATION_MODEL__LANGUAGE=en overrides data['model']['language']."""
    for raw_key, raw_value in environ.items():
        if not raw_key.startswith(ENV_PREFIX):
            continue
        path = raw_key[len(ENV_PREFIX):].lower().split("__")
        if len(path) != 2 or path[0] not in _SECTIONS:
            continue
        section, leaf = path
        if leaf not in {f.name for f in fields(_SECTIONS[section])}:
            raise ConfigError(f"{raw_key} does not name a setting ({section}.{leaf})")
        data.setdefault(section, {})[leaf] = raw_value
    return data


def load_settings(
    path: str | os.PathLike[str] | None = None,
    *,
    environ: dict[str, str] | None = None,
) -> Settings:
    """Load, override, validate. Raises ConfigError on anything questionable."""
    environ = dict(os.environ if environ is None else environ)
    if path is None:
        path = environ.get(f"{ENV_PREFIX}CONFIG")

    data: dict[str, Any] = {}
    if path is not None:
        config_path = Path(path)
        if not config_path.is_file():
            raise ConfigError(f"config file not found: {config_path}")
        loaded = yaml.safe_load(config_path.read_text(encoding="utf-8")) or {}
        if not isinstance(loaded, dict):
            raise ConfigError(f"{config_path} must contain a YAML mapping")
        data = loaded

    unknown_sections = set(data) - set(_SECTIONS)
    if unknown_sections:
        raise ConfigError(f"unknown config section(s): {', '.join(sorted(unknown_sections))}")

    data = _apply_env(data, environ)

    settings = Settings(
        **{
            name: _build(cls, data.get(name, {}) or {}, name)
            for name, cls in _SECTIONS.items()
        }
    )
    settings.validate()
    return settings
