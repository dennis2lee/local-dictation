#!/usr/bin/env python3
"""Measure Whisper on an Intel GPU, before anything is built on top of it.

    openvino-benchmark.py --models ~/models --device GPU

This is the first thing to run on an Arc machine and the only one that has to
come before the rest: every design decision downstream of it — one model or a
draft model beside it, INT8 or FP16, how much audio a decode pass may cover —
is settled by numbers this prints and by nothing else.

What it answers, in order:

  1. Is there an Intel GPU here that OpenVINO can actually reach?
  2. Does large-v3-turbo load and decode on it?
  3. How long does a decode take, against how much audio? (real-time factor)
  4. Does that change with precision — FP16, INT8, INT4?

It decodes through `app.inference.openvino`, the same backend the server runs,
so the numbers describe what would ship rather than a benchmark harness that
resembles it.

Targets to read the output against, from the plan:

    first decode of a short utterance   <= 800 ms, ideally <= 500 ms
    real-time factor                    <  0.25

A real-time factor above 1 means the decoder is slower than the speech it is
transcribing, and no amount of streaming policy recovers from that.

Accuracy is not measured here. Precision costs accuracy in a way only real
speech shows, and Korean is where it shows first — use benchmark.py with a
manifest of clips against a running server for that, and decide precision on
both numbers rather than on this one.

Nothing is written anywhere except the OpenVINO compile cache inside each model
directory, and no audio leaves the process.
"""

from __future__ import annotations

import argparse
import math
import statistics
import struct
import sys
import time
import wave
from pathlib import Path

SAMPLE_RATE = 16000


def find_server_root() -> Path | None:
    """Locate the directory holding the `app` package.

    The repository has this script at server/scripts/ and an install has it at
    <prefix>/app/scripts/, so the depth differs. Walking up for the package
    itself works in both without either needing to be special-cased.
    """
    for directory in Path(__file__).resolve().parents:
        if (directory / "app" / "inference" / "openvino.py").is_file():
            return directory
    return None


def read_wav(path: Path) -> bytes:
    """Read 16 kHz mono 16-bit PCM — the format the client sends."""
    with wave.open(str(path)) as handle:
        if handle.getnchannels() != 1 or handle.getsampwidth() != 2:
            raise ValueError(f"{path}: need mono 16-bit WAV")
        if handle.getframerate() != SAMPLE_RATE:
            raise ValueError(f"{path}: need {SAMPLE_RATE} Hz, got {handle.getframerate()}")
        return handle.readframes(handle.getnframes())


def synthetic(seconds: float) -> bytes:
    """A tone, for when no clip is to hand.

    It transcribes to nothing useful — it is not speech. What it does measure
    honestly is the cost of a decode pass over that much audio, which is the
    number this script exists for. Use --audio with real clips before believing
    anything about accuracy.
    """
    samples = int(SAMPLE_RATE * seconds)
    return b"".join(
        struct.pack("<h", int(8000 * math.sin(2 * math.pi * (200 + 3 * i / SAMPLE_RATE) * i / SAMPLE_RATE)))
        for i in range(samples)
    )


def report_devices() -> list[str]:
    try:
        import openvino
    except ImportError:
        print("openvino is not installed. pip install 'openvino-genai'", file=sys.stderr)
        return []

    core = openvino.Core()
    devices = list(core.available_devices)
    print(f"OpenVINO {openvino.__version__}")
    for device in devices:
        try:
            name = core.get_property(device, "FULL_DEVICE_NAME")
        except Exception:  # noqa: BLE001 - a plugin that will not answer is not fatal
            name = "(no name reported)"
        print(f"  {device:<8} {name}")
    if not any(d.startswith("GPU") for d in devices):
        print("\n  No GPU listed. On an Intel machine that means the graphics driver or the")
        print("  OpenVINO GPU plugin is missing — the CPU entry alone is not enough to")
        print("  measure what this script is for.")
    print()
    return devices


def measure(transcriber, pcm: bytes, *, runs: int) -> tuple[float, float, str]:
    """Decode the same audio `runs` times and report the median.

    The first decode of a session pays for lazy graph work that no later one
    does, so it is discarded rather than averaged in — a mean over three runs
    where one is an outlier describes neither the cold nor the warm case.
    """
    transcriber.transcribe(pcm[: SAMPLE_RATE * 2])  # discarded: warms the graph
    durations, text = [], ""
    for _ in range(runs):
        result = transcriber.transcribe(pcm)
        durations.append(result.duration_seconds)
        text = result.text
    seconds = len(pcm) / (SAMPLE_RATE * 2)
    return statistics.median(durations), seconds, text


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--models",
        type=Path,
        default=Path.cwd(),
        help="directory holding the large-v3-turbo-openvino-* exports "
        "(fetch-model.sh puts them there)",
    )
    parser.add_argument(
        "--device",
        default="GPU",
        help="OpenVINO device to compile for (default: GPU). CPU is useful only "
        "as a baseline to compare the GPU against",
    )
    parser.add_argument(
        "--audio",
        type=Path,
        nargs="*",
        help="16 kHz mono WAV clips. Without these a tone of --seconds is used, "
        "which measures decode cost honestly and says nothing about accuracy",
    )
    parser.add_argument("--seconds", type=float, default=5.0, help="length of the synthetic clip")
    parser.add_argument("--language", choices=["ko", "en"], default="ko")
    parser.add_argument("--runs", type=int, default=3, help="decodes to take the median of")
    parser.add_argument(
        "--devices-only", action="store_true", help="list devices and exit, loading nothing"
    )
    args = parser.parse_args()

    devices = report_devices()
    if args.devices_only:
        return 0 if devices else 1

    root = find_server_root()
    if root is None:
        print("could not find the app package next to this script", file=sys.stderr)
        return 2
    sys.path.insert(0, str(root))

    from app.inference.base import InferenceError
    from app.inference.factory import create_transcriber
    from app.settings import ModelSettings

    exports = sorted(p for p in args.models.glob("*openvino*") if p.is_dir())
    if not exports:
        print(f"no OpenVINO export under {args.models}", file=sys.stderr)
        print("  server/scripts/fetch-model.sh large-v3-turbo-openvino-int8 --dest "
              f"{args.models}", file=sys.stderr)
        return 2

    if args.audio:
        clips = [(path.name, read_wav(path)) for path in args.audio]
    else:
        clips = [(f"tone {args.seconds:g}s", synthetic(args.seconds))]

    print(f"device {args.device}, language {args.language}, median of {args.runs}\n")
    header = f"{'model':<38} {'compile':>9} {'audio':>7} {'decode':>8} {'RTF':>6}"
    print(header)
    print("-" * len(header))

    failures = 0
    for export in exports:
        settings = ModelSettings(path=str(export), device=args.device, language=args.language)
        started = time.monotonic()
        try:
            transcriber = create_transcriber(settings, backend="openvino")
        except InferenceError as exc:
            failures += 1
            print(f"{export.name:<38} {'FAILED':>9}   {exc}")
            continue
        compiled = time.monotonic() - started

        try:
            for label, pcm in clips:
                decode, seconds, text = measure(transcriber, pcm, runs=args.runs)
                rtf = decode / seconds if seconds else float("nan")
                flag = "" if rtf < 0.25 else ("  <- slower than real time" if rtf >= 1 else "  <- over target")
                name = f"{export.name} [{label}]" if len(clips) > 1 else export.name
                print(f"{name:<38} {compiled:>8.1f}s {seconds:>6.1f}s {decode:>7.2f}s {rtf:>6.2f}{flag}")
                if args.audio:
                    print(f"{'':<38} {text[:100]}")
        finally:
            transcriber.close()

    print()
    print("compile time is once per model per machine — it is cached inside the model")
    print("directory, so the second run of this script reports a much smaller number.")
    if failures:
        print(f"\n{failures} model(s) would not load on {args.device}.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
