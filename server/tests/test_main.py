"""`--check` is what an operator runs before a restart, so what it must catch is
a config that names a file the server would then fail to open. It used to answer
"ok" for a model directory that was not there, which is the one answer worse
than not having the command."""

from __future__ import annotations

from pathlib import Path

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
