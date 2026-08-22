"""The MLX backend, tested on machines that cannot run it.

Every CI runner this project has is Linux, where mlx does not exist. So the
parts that can be checked anywhere — the result mapping, the refusal to accept
the other backend's model directory, the preflight and factory wiring — are
separated from the one part that needs the hardware, which skips.
"""

from __future__ import annotations

import importlib.util
from pathlib import Path

import pytest

from app.inference.base import InferenceError
from app.inference.factory import create_transcriber
from app.inference.mlx import WEIGHTS, _result
from app.main import preflight
from app.settings import ModelSettings, Settings, StreamingSettings

HAS_MLX = importlib.util.find_spec("mlx_whisper") is not None


def mlx_model(tmp_path: Path, name: str = "large-v3-turbo-mlx") -> Path:
    directory = tmp_path / name
    directory.mkdir()
    (directory / "weights.safetensors").write_bytes(b"not real weights")
    (directory / "config.json").write_text("{}", encoding="utf-8")
    return directory


def ct2_model(tmp_path: Path, name: str = "large-v3-turbo") -> Path:
    directory = tmp_path / name
    directory.mkdir()
    (directory / "model.bin").write_bytes(b"not real weights either")
    return directory


# -- the mapping ------------------------------------------------------------


def test_a_decode_is_mapped_onto_the_shared_contract():
    """Word timings are the load-bearing part: the streaming layer uses them to
    drop audio it has already committed, which is what keeps the cost of a pass
    flat over a long utterance. A backend that returned text and no words would
    look fine and grow without bound."""
    raw = {
        "text": "  새로운 기능은 다음 주에 나옵니다. ",
        "segments": [
            {
                "end": 3.5,
                "avg_logprob": -0.2,
                "words": [
                    {"word": "새로운", "start": 0.0, "end": 0.4},
                    {"word": "기능은", "start": 0.4, "end": 0.9},
                ],
            },
            {"end": 5.6, "avg_logprob": -0.4, "words": [{"word": "나옵니다", "start": 4.9, "end": 5.6}]},
        ],
    }

    result = _result(raw, audio_seconds=6.0, duration=0.72)

    assert result.text == "새로운 기능은 다음 주에 나옵니다."
    assert [word.text for word in result.words] == ["새로운", "기능은", "나옵니다"]
    assert result.words[-1].end == pytest.approx(5.6)
    assert result.segment_ends == (3.5, 5.6)
    assert result.avg_logprob == pytest.approx(-0.3)
    assert result.real_time_factor == pytest.approx(0.12)


def test_a_decode_that_found_nothing_is_not_an_error():
    result = _result({"text": "", "segments": []}, audio_seconds=1.0, duration=0.1)

    assert result.text == ""
    assert result.words == ()
    assert result.avg_logprob is None


def test_segment_times_survive_as_plain_floats():
    """MLX hands back numpy scalars. They compare fine and serialise to JSON as
    an error, which would surface as a broken `ready` event rather than
    anything pointing at this."""
    numpy = pytest.importorskip("numpy")
    raw = {
        "text": "x",
        "segments": [{"end": numpy.float64(2.5), "avg_logprob": -0.1,
                      "words": [{"word": "x", "start": numpy.float64(0.0),
                                 "end": numpy.float64(2.5)}]}],
    }

    result = _result(raw, audio_seconds=3.0, duration=0.3)

    assert type(result.segment_ends[0]) is float
    assert type(result.words[0].end) is float


# -- refusing the other backend's model -------------------------------------


@pytest.mark.skipif(not HAS_MLX, reason="mlx-whisper is not installed")
def test_a_ctranslate2_directory_is_refused_with_the_reason(tmp_path):
    """The two conversions live under similar names and are not
    interchangeable. Failing here beats failing three seconds into the first
    utterance with a stack trace about safetensors."""
    settings = ModelSettings(path=str(ct2_model(tmp_path)), language="ko")

    with pytest.raises(InferenceError) as caught:
        create_transcriber(settings, backend="mlx")

    message = str(caught.value)
    assert "weights.safetensors" in message
    assert "--backend whisper" in message


@pytest.mark.skipif(not HAS_MLX, reason="mlx-whisper is not installed")
def test_a_missing_directory_is_refused(tmp_path):
    settings = ModelSettings(path=str(tmp_path / "absent"), language="ko")

    with pytest.raises(InferenceError, match="not found"):
        create_transcriber(settings, backend="mlx")


def test_an_unknown_backend_is_still_an_error():
    with pytest.raises(ValueError, match="unknown inference backend"):
        create_transcriber(ModelSettings(), backend="metal")


# -- preflight --------------------------------------------------------------


def settings_for(model: Path, draft: Path | None = None) -> Settings:
    return Settings(
        model=ModelSettings(
            path=str(model),
            draft_path=str(draft) if draft else None,
            language="ko",
        ),
        streaming=StreamingSettings(vad="energy"),
    )


def test_check_wants_the_weights_the_chosen_backend_reads(tmp_path):
    mlx_directory = mlx_model(tmp_path)
    ct2_directory = ct2_model(tmp_path)

    # Each backend is happy with its own conversion...
    assert preflight(settings_for(mlx_directory), backend="mlx")[0] == []
    assert preflight(settings_for(ct2_directory), backend="whisper")[0] == []

    # ...and says which file it wanted when given the other one.
    problems, _ = preflight(settings_for(ct2_directory), backend="mlx")
    assert any("weights.safetensors" in problem for problem in problems)

    problems, _ = preflight(settings_for(mlx_directory), backend="whisper")
    assert any("model.bin" in problem for problem in problems)


def test_check_holds_the_draft_model_to_the_same_format(tmp_path):
    """The draft model runs on the same backend as the accurate one, so a
    config carrying a CTranslate2 draft path is a config that will not start
    under --backend mlx. Someone switching backends hits exactly this."""
    problems, _ = preflight(
        settings_for(mlx_model(tmp_path), draft=ct2_model(tmp_path, "base")),
        backend="mlx",
    )

    assert any("draft_path" in problem and "weights.safetensors" in problem for problem in problems)


def test_older_mlx_conversions_carry_npz_and_are_accepted(tmp_path):
    directory = tmp_path / "base-mlx"
    directory.mkdir()
    (directory / "weights.npz").write_bytes(b"an older conversion")

    assert "weights.npz" in WEIGHTS
    assert preflight(settings_for(directory), backend="mlx")[0] == []


# -- the hardware -----------------------------------------------------------


@pytest.mark.skipif(not HAS_MLX, reason="mlx-whisper is not installed")
def test_the_backend_decodes_on_this_machine(tmp_path):
    """The only test here that needs an Apple Silicon GPU. It decodes silence,
    which is what warmup does, so it proves the wiring without needing a clip
    or an opinion about what the model should have heard."""
    import os

    model = os.environ.get("LD_MLX_MODEL")
    if not model or not Path(model).is_dir():
        pytest.skip("set LD_MLX_MODEL to an MLX conversion to run this")

    transcriber = create_transcriber(
        ModelSettings(path=model, language="ko"), backend="mlx"
    )
    try:
        result = transcriber.transcribe(b"\x00\x00" * 16000)
        assert result.audio_seconds == pytest.approx(1.0)
        assert result.duration_seconds > 0
    finally:
        transcriber.close()
