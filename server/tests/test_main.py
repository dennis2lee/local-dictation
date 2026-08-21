"""`--check` is what an operator runs before a restart, so what it must catch is
a config that names a file the server would then fail to open. It used to answer
"ok" for a model directory that was not there, which is the one answer worse
than not having the command."""

from __future__ import annotations

from pathlib import Path

import pytest

from app.main import cli


def model_dir(tmp_path: Path, name: str = "large-v3-turbo") -> Path:
    directory = tmp_path / name
    directory.mkdir()
    (directory / "model.bin").write_bytes(b"not a real model")
    return directory


def write_config(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "server.yaml"
    path.write_text(body, encoding="utf-8")
    return path


def check(path: Path, *extra: str) -> int:
    return cli(["--config", str(path), "--check", *extra])


def test_a_model_directory_that_is_not_there_fails_the_check(tmp_path, capsys):
    config = write_config(
        tmp_path,
        f'model: {{path: "{tmp_path / "absent"}"}}\nstreaming: {{vad: "energy"}}\n',
    )
    assert check(config) == 1
    assert "model.path is not a directory" in capsys.readouterr().err


def test_a_model_directory_without_weights_fails_the_check(tmp_path, capsys):
    empty = tmp_path / "large-v3-turbo"
    empty.mkdir()
    config = write_config(
        tmp_path, f'model: {{path: "{empty}"}}\nstreaming: {{vad: "energy"}}\n'
    )
    assert check(config) == 1
    assert "holds no model.bin" in capsys.readouterr().err


def test_an_installed_model_passes(tmp_path, capsys):
    config = write_config(
        tmp_path,
        f'model: {{path: "{model_dir(tmp_path)}"}}\nstreaming: {{vad: "energy"}}\n',
    )
    assert check(config) == 0
    assert "ok:" in capsys.readouterr().out


def test_the_fake_backend_needs_no_model(tmp_path):
    config = write_config(
        tmp_path,
        f'model: {{path: "{tmp_path / "absent"}"}}\nstreaming: {{vad: "energy"}}\n',
    )
    assert check(config, "--backend", "fake") == 0


def test_a_draft_model_that_is_not_there_fails_the_check(tmp_path, capsys):
    config = write_config(
        tmp_path,
        f'model: {{path: "{model_dir(tmp_path)}", draft_path: "{tmp_path / "base"}"}}\n'
        'streaming: {vad: "energy"}\n',
    )
    assert check(config) == 1
    assert "model.draft_path" in capsys.readouterr().err


def test_an_unreadable_certificate_fails_the_check(tmp_path, capsys):
    # The shipped configs enable TLS, so a first bring-up that has not put
    # certificates in place yet is the common case — and uvicorn would die on it.
    config = write_config(
        tmp_path,
        f'model: {{path: "{model_dir(tmp_path)}"}}\n'
        'streaming: {vad: "energy"}\n'
        f'security: {{tls_certificate: "{tmp_path / "server.crt"}", '
        f'tls_private_key: "{tmp_path / "server.key"}"}}\n',
    )
    assert check(config) == 1
    problems = capsys.readouterr().err
    assert "security.tls_certificate is not readable" in problems
    assert "security.tls_private_key is not readable" in problems


def test_a_missing_vad_model_warns_but_passes(tmp_path, capsys):
    # The server falls back to the energy detector rather than refusing to
    # serve, so this must not be reported as a failure.
    config = write_config(
        tmp_path,
        f'model: {{path: "{model_dir(tmp_path)}"}}\n'
        f'streaming: {{vad: "silero", silero_model_path: "{tmp_path / "silero.onnx"}"}}\n',
    )
    assert check(config) == 0
    assert "energy detector" in capsys.readouterr().err


def test_a_model_that_cannot_be_loaded_is_reported_not_raised(tmp_path, capsys):
    """A model.bin that exists and is not loadable — a truncated download, or a
    conversion from a newer CTranslate2 — used to reach the log as a traceback
    from inside the backend. It gets the same one-line answer as every other
    startup failure."""
    from app.inference.base import InferenceError
    from app.inference.whisper import FasterWhisperTranscriber
    from app.settings import ModelSettings

    directory = tmp_path / "large-v3-turbo"
    directory.mkdir()
    (directory / "model.bin").write_text("not a model at all")

    try:
        import faster_whisper  # noqa: F401
    except ImportError:
        pytest.skip("the inference backend is not installed")

    with pytest.raises(InferenceError) as caught:
        FasterWhisperTranscriber(ModelSettings(path=str(directory)))
    assert "could not be loaded" in str(caught.value)


def check_config(path: Path, *extra: str) -> int:
    return cli(["--config", str(path), "--check-config", *extra])


def test_a_config_validates_on_a_machine_that_could_not_serve_it(tmp_path, capsys):
    """The question CI asks of the shipped configs.

    They name `/opt/local-dictation/models/large-v3-turbo` and a TLS keypair,
    none of which exists on a runner. `--check` is right to refuse that machine
    and wrong as a test of the file, so the two are separate questions.
    """
    config = write_config(
        tmp_path,
        f'model: {{path: "{tmp_path / "absent"}"}}\n'
        "security:\n"
        f'  tls_certificate: "{tmp_path / "absent.crt"}"\n'
        f'  tls_private_key: "{tmp_path / "absent.key"}"\n',
    )

    assert check_config(config) == 0
    assert "is valid" in capsys.readouterr().out
    # And the other question still gets the other answer.
    assert check(config) == 1


def test_an_invalid_config_still_fails_the_config_check(tmp_path, capsys):
    config = write_config(tmp_path, "server: {port: -1}\n")

    assert check_config(config) == 2
    assert "configuration error" in capsys.readouterr().err


def test_the_shipped_configs_are_valid():
    """They ship to every install, and nothing else opens them until a server
    starts with one."""
    shipped = Path(__file__).resolve().parent.parent / "config"
    for name in ("server-ko.yaml", "server-en.yaml"):
        assert check_config(shipped / name) == 0
