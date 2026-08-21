#!/usr/bin/env python3
"""Measure accuracy and streaming latency against a running server.

    benchmark.py --manifest cases.jsonl --host 127.0.0.1 --port 8765

The manifest is one JSON object per line:

    {"audio": "clips/ko-001.wav", "reference": "오늘 오후 세 시에 회의를 시작합니다.", "language": "ko"}

Audio must be 16 kHz mono 16-bit WAV — the same format the client sends. Each
clip is streamed at real time through the real WebSocket protocol, because the
numbers that matter are the ones a user experiences: a batch decode of a whole
file measures something nobody does.

Reports CER and WER per clip and overall, plus first-partial and finalization
latency percentiles against the project plan's targets.

Read CER for Korean and WER for English. Korean is written without consistent
word spacing and Whisper renders numbers as digits, so a single "세 시" coming
back as "3시" moves WER enormously on a short clip while CER barely notices.
English words are the unit a reader actually perceives, so WER is the honest
number there.

Nothing is written anywhere except the report you ask for with --out.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import re
import ssl
import statistics
import sys
import time
import unicodedata
import wave
from dataclasses import dataclass, field
from pathlib import Path

try:
    import websockets
except ImportError:  # pragma: no cover - depends on the host install
    print("benchmark.py needs the websockets package (pip install websockets)", file=sys.stderr)
    raise SystemExit(2)

SAMPLE_RATE = 16000
FRAME_MS = 20
FRAME_BYTES = SAMPLE_RATE * 2 * FRAME_MS // 1000

# The plan's targets, so a run says whether it met them rather than leaving the
# reader to compare numbers to a document.
TARGET_FIRST_PARTIAL_P95 = 2.0
TARGET_FINALIZATION_P95 = 1.5


@dataclass
class Case:
    audio: Path
    reference: str
    language: str


@dataclass
class Result:
    case: Case
    hypothesis: str = ""
    first_partial: float | None = None
    finalization: float | None = None
    partials: int = 0
    audio_seconds: float = 0.0
    error: str = ""
    cer: float = field(default=0.0)
    wer: float = field(default=0.0)


# --------------------------------------------------------------------------
# Scoring
# --------------------------------------------------------------------------


def normalize(text: str) -> str:
    """Fold away differences nobody would call a transcription error.

    NFKC so composed and decomposed Hangul compare equal, case folding, and
    punctuation stripped — Whisper's punctuation is a stylistic choice, not
    something a word error rate should punish.
    """
    text = unicodedata.normalize("NFKC", text).lower()
    text = re.sub(r"[^\w\s]", " ", text)
    return re.sub(r"\s+", " ", text).strip()


def edit_distance(reference: list[str], hypothesis: list[str]) -> int:
    """Levenshtein distance, two rows at a time."""
    if not reference:
        return len(hypothesis)
    previous = list(range(len(reference) + 1))
    for j, hypothesis_token in enumerate(hypothesis, start=1):
        current = [j]
        for i, reference_token in enumerate(reference, start=1):
            current.append(
                previous[i - 1]
                if reference_token == hypothesis_token
                else 1 + min(previous[i - 1], previous[i], current[i - 1])
            )
        previous = current
    return previous[-1]


def error_rate(reference: str, hypothesis: str, *, by_character: bool) -> float:
    reference_tokens = list(normalize(reference).replace(" ", "")) if by_character else normalize(reference).split()
    hypothesis_tokens = list(normalize(hypothesis).replace(" ", "")) if by_character else normalize(hypothesis).split()
    if not reference_tokens:
        return 0.0 if not hypothesis_tokens else 1.0
    return edit_distance(reference_tokens, hypothesis_tokens) / len(reference_tokens)


# --------------------------------------------------------------------------
# Streaming one case
# --------------------------------------------------------------------------


def read_wav(path: Path) -> bytes:
    with wave.open(str(path)) as handle:
        if handle.getnchannels() != 1 or handle.getframerate() != SAMPLE_RATE or handle.getsampwidth() != 2:
            raise ValueError(
                f"{path} is {handle.getnchannels()} ch / {handle.getframerate()} Hz / "
                f"{handle.getsampwidth() * 8} bit; need 1 / {SAMPLE_RATE} / 16"
            )
        return handle.readframes(handle.getnframes())


async def run_case(case: Case, endpoint: str, ssl_context: ssl.SSLContext | None, index: int) -> Result:
    result = Result(case=case)
    try:
        pcm = read_wav(case.audio)
    except (OSError, ValueError) as exc:
        result.error = str(exc)
        return result
    result.audio_seconds = len(pcm) / (SAMPLE_RATE * 2)

    try:
        async with websockets.connect(endpoint, ssl=ssl_context, max_size=2**20) as socket:
            await socket.send(
                json.dumps(
                    {
                        "type": "start",
                        "protocol_version": 1,
                        "session_id": f"s-bench-{index:04d}",
                        "client_version": "benchmark",
                        "language": case.language,
                        "audio": {"encoding": "pcm_s16le", "sample_rate": SAMPLE_RATE, "channels": 1},
                    }
                )
            )
            acknowledgement = json.loads(await asyncio.wait_for(socket.recv(), 60))
            if acknowledgement.get("type") != "ready":
                result.error = f"{acknowledgement.get('code', acknowledgement.get('type'))}: {acknowledgement.get('message', '')}"
                return result

            events: list[dict] = []
            started = time.monotonic()

            async def reader() -> None:
                async for raw in socket:
                    event = json.loads(raw)
                    if event["type"] == "transcript":
                        event["_at"] = time.monotonic() - started
                        events.append(event)
                    elif event["type"] == "error" and event.get("fatal"):
                        result.error = f"{event['code']}: {event['message']}"
                        return
                    elif event["type"] == "closed":
                        return

            reading = asyncio.create_task(reader())

            # Real time, 20 ms at a time: the same cadence a microphone produces.
            for offset in range(0, len(pcm), FRAME_BYTES):
                await socket.send(pcm[offset : offset + FRAME_BYTES])
                await asyncio.sleep(FRAME_MS / 1000)
            await socket.send(b"\x00\x00" * SAMPLE_RATE)  # a second of silence

            flushed_at = time.monotonic()
            await socket.send(json.dumps({"type": "flush"}))
            for _ in range(600):
                if any(event.get("final") for event in events):
                    break
                await asyncio.sleep(0.1)
            finalized_at = time.monotonic()

            await socket.send(json.dumps({"type": "stop"}))
            try:
                await asyncio.wait_for(reading, timeout=15)
            except TimeoutError:
                reading.cancel()

            if events:
                result.first_partial = events[0]["_at"]
            result.partials = sum(1 for event in events if not event["final"])
            result.finalization = finalized_at - flushed_at
            result.hypothesis = " ".join(event["stable"] for event in events if event.get("final")).strip()

    except Exception as exc:  # noqa: BLE001 - one bad clip must not end the run
        result.error = f"{type(exc).__name__}: {exc}"
        return result

    if not result.error:
        result.cer = error_rate(case.reference, result.hypothesis, by_character=True)
        result.wer = error_rate(case.reference, result.hypothesis, by_character=False)
    return result


# --------------------------------------------------------------------------
# Reporting
# --------------------------------------------------------------------------


def percentile(values: list[float], fraction: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = min(int(fraction * len(ordered)), len(ordered) - 1)
    return ordered[index]


def report(results: list[Result]) -> str:
    lines: list[str] = []
    good = [r for r in results if not r.error]
    failed = [r for r in results if r.error]

    lines.append("Per clip")
    lines.append(f"  {'clip':32} {'lang':5} {'CER':>7} {'WER':>7} {'1st':>7} {'final':>7}")
    for result in results:
        if result.error:
            lines.append(f"  {result.case.audio.name:32} {result.case.language:5} FAILED  {result.error[:60]}")
            continue
        first = f"{result.first_partial:.2f}s" if result.first_partial is not None else "  —  "
        final = f"{result.finalization:.2f}s" if result.finalization is not None else "  —  "
        lines.append(
            f"  {result.case.audio.name:32} {result.case.language:5} "
            f"{result.cer:6.1%} {result.wer:6.1%} {first:>7} {final:>7}"
        )

    if not good:
        lines.append("\nNo clip completed.")
        return "\n".join(lines)

    lines.append("\nAccuracy")
    for language in sorted({r.case.language for r in good}):
        subset = [r for r in good if r.case.language == language]
        lines.append(
            f"  {language}: CER {statistics.mean(r.cer for r in subset):.1%}  "
            f"WER {statistics.mean(r.wer for r in subset):.1%}  ({len(subset)} clip(s))"
        )
    lines.append(
        f"  all: CER {statistics.mean(r.cer for r in good):.1%}  "
        f"WER {statistics.mean(r.wer for r in good):.1%}"
    )

    first_partials = [r.first_partial for r in good if r.first_partial is not None]
    finalizations = [r.finalization for r in good if r.finalization is not None]

    lines.append("\nLatency")
    for label, values, target in (
        ("first partial", first_partials, TARGET_FIRST_PARTIAL_P95),
        ("finalization", finalizations, TARGET_FINALIZATION_P95),
    ):
        p50, p95 = percentile(values, 0.5), percentile(values, 0.95)
        if p95 is None:
            lines.append(f"  {label:14} no measurements")
            continue
        verdict = "meets" if p95 <= target else "MISSES"
        lines.append(
            f"  {label:14} p50 {p50:.2f}s  p95 {p95:.2f}s   "
            f"target p95 <= {target}s — {verdict}"
        )

    audio = sum(r.audio_seconds for r in good)
    lines.append(f"\n{len(good)} clip(s), {audio:.1f}s of audio, {len(failed)} failure(s)")
    return "\n".join(lines)


# --------------------------------------------------------------------------


def load_manifest(path: Path, default_language: str) -> list[Case]:
    cases: list[Case] = []
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        try:
            entry = json.loads(line)
        except ValueError as exc:
            raise SystemExit(f"{path}:{number}: {exc}") from exc
        audio = Path(entry["audio"])
        if not audio.is_absolute():
            audio = path.parent / audio
        cases.append(
            Case(audio=audio, reference=entry["reference"], language=entry.get("language", default_language))
        )
    if not cases:
        raise SystemExit(f"{path} has no cases")
    return cases


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--manifest", required=True, type=Path, help="JSONL file of clips and references")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument("--language", default="ko", help="default when a case does not say")
    parser.add_argument("--tls", action="store_true", help="connect with wss")
    parser.add_argument("--insecure", action="store_true", help="do not verify the server certificate")
    parser.add_argument("--out", type=Path, help="also write the report here")
    args = parser.parse_args()

    cases = load_manifest(args.manifest, args.language)

    scheme = "wss" if args.tls else "ws"
    endpoint = f"{scheme}://{args.host}:{args.port}/v1/dictation"

    ssl_context = None
    if args.tls and args.insecure:
        ssl_context = ssl.create_default_context()
        ssl_context.check_hostname = False
        ssl_context.verify_mode = ssl.CERT_NONE

    print(f"{len(cases)} clip(s) against {endpoint}, streamed at real time\n")

    async def run_all() -> list[Result]:
        results = []
        for index, case in enumerate(cases):
            print(f"  [{index + 1}/{len(cases)}] {case.audio.name}", flush=True)
            # Sequentially: concurrent sessions would measure contention rather
            # than latency, and the server refuses beyond its capacity anyway.
            results.append(await run_case(case, endpoint, ssl_context, index))
        return results

    results = asyncio.run(run_all())

    text = report(results)
    print("\n" + text)
    if args.out:
        args.out.write_text(text + "\n", encoding="utf-8")
        print(f"\nwritten to {args.out}")

    return 1 if any(r.error for r in results) else 0


if __name__ == "__main__":
    raise SystemExit(main())
