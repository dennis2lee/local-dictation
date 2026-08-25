"""The OpenVINO backend, tested on machines that cannot run it.

Every CI runner this project has is Linux with no Intel GPU, and it is
developed on a Mac. So the parts that can be checked anywhere — the result
mapping, device normalisation, the refusal of an ambiguous device, the refusal
of the other backends' model directories, and the preflight and factory
wiring — are separated from the parts that need the hardware, which skip.

The same split the MLX backend uses, for the same reason.
"""

from __future__ import annotations

import importlib.util
from dataclasses import dataclass
from pathlib import Path

import pytest

from app.inference.base import InferenceError
from app.inference.factory import create_transcriber
from app.inference.openvino import WEIGHTS, _result, device_family, normalise_device
from app.main import preflight
from app.settings import ModelSettings, Settings, StreamingSettings

HAS_OPENVINO = importlib.util.find_spec("openvino_genai") is not None


# -- stand-ins for what openvino-genai returns ------------------------------
#
# Plain dataclasses rather than mocks: the mapper reads three attribute names
# off each item, and a dataclass that carries exactly those is a more honest
# description of the contract than a Mock that answers to anything.


@dataclass
class Chunk:
    text: str
    start_ts: float
    end_ts: float


@dataclass
class Decoded:
    """openvino-genai's result.

    `chunks` and `words` are grouped by generated sequence, so each is a list
    of lists — `words[0]` is the first sequence's words, not the first word.
    That shape is what the runtime actually returns; reading it as a flat list
    raises AttributeError on the first decode, which is how it was found.
    """

    text: str
    chunks: tuple = ()
    words: tuple = ()

    def __post_init__(self) -> None:
        object.__setattr__(self, "texts", [self.text])

    def __str__(self) -> str:
        return self.text


def one_sequence(*items) -> tuple:
    """Wrap items the way the runtime groups them: one list per sequence."""
    return ([*items],)


def ov_model(tmp_path: Path, name: str = "large-v3-turbo-openvino-int8") -> Path:
    directory = tmp_path / name
    directory.mkdir()
    (directory / "openvino_encoder_model.xml").write_text("<net/>", encoding="utf-8")
    (directory / "openvino_encoder_model.bin").write_bytes(b"not real weights")
    return directory


def ct2_model(tmp_path: Path, name: str = "large-v3-turbo") -> Path:
    directory = tmp_path / name
    directory.mkdir()
    (directory / "model.bin").write_bytes(b"not real weights either")
    return directory


def mlx_model(tmp_path: Path, name: str = "large-v3-turbo-mlx") -> Path:
    directory = tmp_path / name
    directory.mkdir()
    (directory / "weights.safetensors").write_bytes(b"nor these")
    return directory


# -- the mapping ------------------------------------------------------------


def test_a_decode_is_mapped_onto_the_shared_contract():
    """Word timings are the load-bearing part: the streaming layer uses them to
    drop audio it has already committed, which is what keeps the cost of a pass
    flat over a long utterance. A backend that returned text and no words would
    look fine and grow without bound."""
    raw = Decoded(
        text="  새로운 기능은 다음 주에 나옵니다. ",
        chunks=one_sequence(Chunk("새로운 기능은", 0.0, 3.5), Chunk("다음 주에 나옵니다", 3.5, 5.6)),
        words=one_sequence(
            Chunk("새로운", 0.0, 0.4),
            Chunk("기능은", 0.4, 0.9),
            Chunk("나옵니다", 4.9, 5.6),
        ),
    )

    result = _result(raw, audio_seconds=6.0, duration=0.72)

    assert result.text == "새로운 기능은 다음 주에 나옵니다."
    assert [word.text for word in result.words] == ["새로운", "기능은", "나옵니다"]
    assert result.words[-1].end == pytest.approx(5.6)
    assert result.segment_ends == (3.5, 5.6)
    assert result.real_time_factor == pytest.approx(0.12)


def test_segments_stand_in_for_words_when_the_runtime_gives_no_words():
    """Not every openvino-genai build returns word timings — they need the
    pipeline to be built with word_timestamps, which decomposes the decoder's
    cross-attention. Without that the streaming layer would never trim its
    decode window and every long utterance would slow down as it grew. Carrying
    each segment as one Word trims at segment boundaries instead: coarser, and
    bounded, which is the property that matters."""
    raw = Decoded(
        text="한 문장. 또 한 문장.",
        chunks=one_sequence(Chunk("한 문장.", 0.0, 2.0), Chunk("또 한 문장.", 2.0, 4.0)),
        words=(),
    )

    result = _result(raw, audio_seconds=4.0, duration=0.5)

    assert [word.text for word in result.words] == ["한 문장.", "또 한 문장."]
    assert result.words[-1].end == pytest.approx(4.0)
    assert result.segment_ends == (2.0, 4.0)


def test_the_flat_shape_the_documentation_describes_still_maps():
    """The runtime groups chunks and words per sequence; the published API
    reference describes them flat. Both are read rather than one being assumed,
    because getting it wrong is an AttributeError on the first real decode and
    nothing earlier catches it."""
    raw = Decoded(
        text="flat",
        chunks=(Chunk("flat", 0.0, 1.0),),
        words=(Chunk("flat", 0.0, 1.0),),
    )

    result = _result(raw, audio_seconds=1.0, duration=0.1)

    assert [word.text for word in result.words] == ["flat"]
    assert result.segment_ends == (1.0,)


def test_a_decode_that_found_nothing_is_not_an_error():
    result = _result(Decoded(text=""), audio_seconds=1.0, duration=0.1)

    assert result.text == ""
    assert result.words == ()
    assert result.avg_logprob is None


def test_timings_survive_as_plain_floats():
    """The runtime hands back its own numeric types. They compare fine and
    serialise to JSON as an error, which would surface as a broken `ready`
    event rather than anything pointing at this."""
    numpy = pytest.importorskip("numpy")
    raw = Decoded(
        text="x",
        chunks=one_sequence(Chunk("x", numpy.float64(0.0), numpy.float64(2.5))),
        words=one_sequence(Chunk("x", numpy.float64(0.0), numpy.float64(2.5))),
    )

    result = _result(raw, audio_seconds=3.0, duration=0.3)

    assert type(result.segment_ends[0]) is float
    assert type(result.words[0].end) is float


# -- device strings ---------------------------------------------------------


@pytest.mark.parametrize(
    ("configured", "expected"),
    [
        ("gpu", "GPU"),
        ("GPU", "GPU"),
        (" gpu ", "GPU"),
        ("intel-gpu", "GPU"),
        ("arc", "GPU"),
        ("gpu.1", "GPU.1"),
        ("cpu", "CPU"),
        ("npu", "NPU"),
        ("", "CPU"),
        # The device-list form uses a colon, not a dot. Splitting on only one
        # of them let MULTI:GPU,CPU past the refusal below.
        ("multi:gpu,cpu", "MULTI:GPU,CPU"),
        ("intel-gpu.1", "GPU.1"),
    ],
)
def test_device_names_are_normalised_onto_openvinos_own(configured, expected):
    assert normalise_device(configured) == expected


@pytest.mark.parametrize(
    ("device", "family"),
    [("GPU", "GPU"), ("GPU.1", "GPU"), ("MULTI:GPU,CPU", "MULTI"), ("AUTO:GPU", "AUTO")],
)
def test_a_device_is_recognised_by_its_family(device, family):
    """Both suffix forms have to be stripped to decide whether a device is one
    this backend accepts — which is what the refusal below turns on."""
    assert device_family(device) == family


def test_an_unknown_device_is_passed_through_rather_than_rejected():
    """OpenVINO grows device names faster than this file can. Passing an
    unrecognised one through means a new accelerator works by configuration,
    and the runtime — not this mapping — is what says it does not exist."""
    assert normalise_device("some-future-thing") == "SOME-FUTURE-THING"


# -- refusing to guess ------------------------------------------------------


@pytest.mark.skipif(not HAS_OPENVINO, reason="openvino-genai is not installed")
@pytest.mark.parametrize("device", ["AUTO", "auto", "HETERO", "MULTI:GPU,CPU"])
def test_a_device_that_lets_openvino_choose_is_refused(tmp_path, device):
    """The whole reason to pick the GPU is knowing you got it. AUTO silently
    lands on the CPU when the GPU plugin fails to load, and the only symptom is
    that dictation is slow — so it is refused rather than accepted."""
    settings = ModelSettings(path=str(ov_model(tmp_path)), device=device, language="ko")

    with pytest.raises(InferenceError) as caught:
        create_transcriber(settings, backend="openvino")

    assert "Name the one you want" in str(caught.value)


@pytest.mark.skipif(not HAS_OPENVINO, reason="openvino-genai is not installed")
def test_the_other_backends_directories_are_refused_with_the_reason(tmp_path):
    """The three conversions live under similar names and none is
    interchangeable. Failing here beats failing three seconds into the first
    utterance with a stack trace about an XML parse."""
    for directory in (ct2_model(tmp_path), mlx_model(tmp_path)):
        settings = ModelSettings(path=str(directory), device="cpu", language="ko")

        with pytest.raises(InferenceError) as caught:
            create_transcriber(settings, backend="openvino")

        assert "openvino_encoder_model.xml" in str(caught.value)


@pytest.mark.skipif(not HAS_OPENVINO, reason="openvino-genai is not installed")
def test_a_missing_directory_is_refused(tmp_path):
    settings = ModelSettings(path=str(tmp_path / "absent"), device="cpu", language="ko")

    with pytest.raises(InferenceError, match="not found"):
        create_transcriber(settings, backend="openvino")


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
    openvino_directory = ov_model(tmp_path)
    ct2_directory = ct2_model(tmp_path)

    # Each backend is happy with its own conversion...
    assert preflight(settings_for(openvino_directory), backend="openvino")[0] == []
    assert preflight(settings_for(ct2_directory), backend="whisper")[0] == []

    # ...and says which file it wanted when given the other one.
    problems, _ = preflight(settings_for(ct2_directory), backend="openvino")
    assert any("openvino_encoder_model.xml" in problem for problem in problems)

    problems, _ = preflight(settings_for(openvino_directory), backend="whisper")
    assert any("model.bin" in problem for problem in problems)


def test_check_holds_the_draft_model_to_the_same_format(tmp_path):
    """The draft model runs on the same backend as the accurate one, so a
    config carrying a CTranslate2 draft path is a config that will not start
    under --backend openvino. Someone switching backends hits exactly this."""
    problems, _ = preflight(
        settings_for(ov_model(tmp_path), draft=ct2_model(tmp_path, "base")),
        backend="openvino",
    )

    assert any(
        "draft_path" in problem and "openvino_encoder_model.xml" in problem
        for problem in problems
    )


def test_the_encoder_is_what_marks_an_openvino_export(tmp_path):
    """Every export produces it under this name, whatever the precision, so it
    is the one file worth looking for."""
    assert WEIGHTS == ("openvino_encoder_model.xml",)


# -- the hardware -----------------------------------------------------------


@pytest.mark.skipif(not HAS_OPENVINO, reason="openvino-genai is not installed")
def test_the_backend_decodes_on_this_machine():
    """The only test here that needs an Intel GPU and a real export. It decodes
    silence, which is what warmup does, so it proves the wiring without needing
    a clip or an opinion about what the model should have heard.

    Set LD_OPENVINO_MODEL to an export and LD_OPENVINO_DEVICE to GPU on the Arc
    machine; without them there is nothing to run against."""
    import os

    model = os.environ.get("LD_OPENVINO_MODEL")
    if not model or not Path(model).is_dir():
        pytest.skip("set LD_OPENVINO_MODEL to an OpenVINO export to run this")

    transcriber = create_transcriber(
        ModelSettings(
            path=model,
            device=os.environ.get("LD_OPENVINO_DEVICE", "CPU"),
            language="ko",
        ),
        backend="openvino",
    )
    try:
        result = transcriber.transcribe(b"\x00\x00" * 16000)
        assert result.audio_seconds == pytest.approx(1.0)
        assert result.duration_seconds > 0
    finally:
        transcriber.close()
