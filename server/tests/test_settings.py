from __future__ import annotations

import pytest

from app.settings import ConfigError, load_settings


def write(tmp_path, body: str):
    path = tmp_path / "server.yaml"
    path.write_text(body, encoding="utf-8")
    return path


def test_defaults_are_valid_and_private():
    settings = load_settings(None, environ={})
    assert settings.language == "ko"
    assert settings.logging.store_audio is False
    assert settings.logging.store_transcript is False


def test_yaml_is_loaded_and_typed(tmp_path):
    path = write(
        tmp_path,
        """
        server: {host: "127.0.0.1", port: 9000}
        model: {language: "en", beam_size: 3}
        limits: {max_sessions: 4}
        """,
    )
    settings = load_settings(path, environ={})
    assert (settings.server.host, settings.server.port) == ("127.0.0.1", 9000)
    assert settings.model.language == "en"
    assert settings.model.beam_size == 3
    assert settings.limits.max_sessions == 4


def test_env_overrides_yaml_and_is_coerced(tmp_path):
    path = write(tmp_path, "server: {port: 9000}\nmodel: {language: 'ko'}\n")
    settings = load_settings(
        path,
        environ={
            "LOCAL_DICTATION_SERVER__PORT": "9100",
            "LOCAL_DICTATION_MODEL__LANGUAGE": "en",
            "LOCAL_DICTATION_LOGGING__STORE_AUDIO": "true",
        },
    )
    assert settings.server.port == 9100
    assert isinstance(settings.server.port, int)
    assert settings.model.language == "en"
    assert settings.logging.store_audio is True


@pytest.mark.parametrize(
    "body",
    [
        "loging: {level: DEBUG}",  # section typo
        "logging: {store_audios: false}",  # key typo, would silently disable the guarantee
        "model: {language: fr}",
        "limits: {max_sessions: 0}",
        "streaming: {chunk_ms: 10}",
        "streaming: {agreement_window: 1}",
        "security: {tls_certificate: /tmp/a.crt}",  # key missing
        "security: {require_client_certificate: true}",  # no CA, no TLS
    ],
)
def test_bad_config_is_rejected(tmp_path, body):
    with pytest.raises(ConfigError):
        load_settings(write(tmp_path, body), environ={})


def test_missing_file_is_an_error(tmp_path):
    with pytest.raises(ConfigError, match="not found"):
        load_settings(tmp_path / "absent.yaml", environ={})


def test_shipped_configs_are_valid():
    from tests.conftest import SERVER_ROOT

    ko = load_settings(SERVER_ROOT / "config" / "server-ko.yaml", environ={})
    en = load_settings(SERVER_ROOT / "config" / "server-en.yaml", environ={})

    assert (ko.language, ko.server.port) == ("ko", 8765)
    assert (en.language, en.server.port) == ("en", 8766)
    # The retention guarantee. These two are the plan's, they are not
    # negotiable, and nothing in the shipped configs may turn them on.
    assert not any((ko.logging.store_audio, ko.logging.store_transcript))
    assert not any((en.logging.store_audio, en.logging.store_transcript))

    # The network posture, which is a trade rather than a guarantee: open and
    # unencrypted, because the deployment this exists for is one machine
    # holding the model and laptops dictating into it, and the loopback default
    # this replaced meant every such install began with the same hand edit to
    # two files. The install and both server docs say it out loud. Tighten it
    # here, deliberately, if that ever stops being the right call.
    for settings in (ko, en):
        assert settings.server.host == "0.0.0.0"
        assert settings.security.tls_certificate is None
        assert settings.security.tls_private_key is None
        assert settings.security.client_ca is None
        assert settings.security.require_client_certificate is False
    # Identical everywhere it matters: same model, same tuning, same limits.
    assert ko.model.path == en.model.path
    assert ko.streaming == en.streaming
    assert ko.limits == en.limits
