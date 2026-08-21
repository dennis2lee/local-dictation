"""In-process metrics, exposed in Prometheus text format.

Deliberately dependency-free: on a closed network the monitoring stack is
whatever the site already runs, and a plain text endpoint is the one thing every
collector can read. Every series here is a timing or a count — the observability
table in the project plan lists exactly these, all marked "content not stored".
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field

#: Latency buckets in seconds. Chosen around the plan's targets: first partial
#: P95 <= 2 s, finalization P95 <= 1.5 s.
LATENCY_BUCKETS: tuple[float, ...] = (0.25, 0.5, 0.75, 1.0, 1.5, 2.0, 3.0, 5.0, 10.0)
RTF_BUCKETS: tuple[float, ...] = (0.25, 0.5, 0.75, 1.0, 1.5, 2.0, 4.0)


@dataclass
class Histogram:
    buckets: tuple[float, ...]
    counts: list[int] = field(default_factory=list)
    total: float = 0.0
    count: int = 0

    def __post_init__(self) -> None:
        if not self.counts:
            self.counts = [0] * (len(self.buckets) + 1)

    def observe(self, value: float) -> None:
        self.total += value
        self.count += 1
        for index, edge in enumerate(self.buckets):
            if value <= edge:
                self.counts[index] += 1
                return
        self.counts[-1] += 1

    def cumulative(self) -> list[tuple[str, int]]:
        running = 0
        rows: list[tuple[str, int]] = []
        for edge, bucket_count in zip(self.buckets, self.counts, strict=False):
            running += bucket_count
            rows.append((_format_float(edge), running))
        rows.append(("+Inf", self.count))
        return rows

    def quantile(self, q: float) -> float | None:
        """Bucket-resolution estimate; good enough for an alert threshold."""
        if self.count == 0:
            return None
        target = q * self.count
        running = 0
        for edge, bucket_count in zip(self.buckets, self.counts, strict=False):
            running += bucket_count
            if running >= target:
                return edge
        return float("inf")


def _format_float(value: float) -> str:
    return f"{value:g}"


class Metrics:
    """Thread-safe: decode timings are recorded from the executor's threads."""

    def __init__(self, *, language: str, model: str) -> None:
        self.language = language
        self.model = model
        self._lock = threading.Lock()

        self.first_partial = Histogram(LATENCY_BUCKETS)
        self.finalization = Histogram(LATENCY_BUCKETS)
        self.real_time_factor = Histogram(RTF_BUCKETS)

        self.sessions_accepted = 0
        self.sessions_rejected = 0
        self.sessions_active = 0
        self.utterances_total = 0
        self.decodes_total = 0
        self.audio_seconds_total = 0.0
        self.decode_seconds_total = 0.0
        self.errors_by_code: dict[str, int] = {}

    # -- recording ---------------------------------------------------------

    def observe_decode(self, *, audio_seconds: float, duration_seconds: float) -> None:
        with self._lock:
            self.decodes_total += 1
            self.audio_seconds_total += audio_seconds
            self.decode_seconds_total += duration_seconds
            if audio_seconds > 0:
                self.real_time_factor.observe(duration_seconds / audio_seconds)

    def observe_first_partial(self, seconds: float) -> None:
        with self._lock:
            self.first_partial.observe(seconds)

    def observe_finalization(self, seconds: float) -> None:
        with self._lock:
            self.finalization.observe(seconds)

    def count_error(self, code: str) -> None:
        with self._lock:
            self.errors_by_code[code] = self.errors_by_code.get(code, 0) + 1

    def count_utterance(self) -> None:
        with self._lock:
            self.utterances_total += 1

    def session_started(self) -> None:
        with self._lock:
            self.sessions_accepted += 1
            self.sessions_active += 1

    def session_finished(self) -> None:
        with self._lock:
            self.sessions_active = max(0, self.sessions_active - 1)

    def session_rejected(self) -> None:
        with self._lock:
            self.sessions_rejected += 1

    # -- exposition --------------------------------------------------------

    def render(self) -> str:
        with self._lock:
            labels = f'language="{self.language}",model="{self.model}"'
            lines: list[str] = []

            def counter(name: str, help_text: str, value: float) -> None:
                lines.append(f"# HELP {name} {help_text}")
                lines.append(f"# TYPE {name} counter")
                lines.append(f"{name}{{{labels}}} {value:g}")

            def gauge(name: str, help_text: str, value: float) -> None:
                lines.append(f"# HELP {name} {help_text}")
                lines.append(f"# TYPE {name} gauge")
                lines.append(f"{name}{{{labels}}} {value:g}")

            def histogram(name: str, help_text: str, hist: Histogram) -> None:
                lines.append(f"# HELP {name} {help_text}")
                lines.append(f"# TYPE {name} histogram")
                for edge, cumulative in hist.cumulative():
                    lines.append(f'{name}_bucket{{{labels},le="{edge}"}} {cumulative}')
                lines.append(f"{name}_sum{{{labels}}} {hist.total:g}")
                lines.append(f"{name}_count{{{labels}}} {hist.count}")

            gauge("dictation_sessions_active", "Sessions currently decoding.", self.sessions_active)
            counter("dictation_sessions_accepted_total", "Sessions admitted.", self.sessions_accepted)
            counter(
                "dictation_sessions_rejected_total",
                "Sessions refused because the server was at capacity.",
                self.sessions_rejected,
            )
            counter("dictation_utterances_total", "Utterances finalized.", self.utterances_total)
            counter("dictation_decodes_total", "Decode passes run.", self.decodes_total)
            counter(
                "dictation_audio_seconds_total", "Audio decoded, in seconds.", self.audio_seconds_total
            )
            counter(
                "dictation_decode_seconds_total",
                "Wall-clock spent decoding, in seconds.",
                self.decode_seconds_total,
            )
            histogram(
                "dictation_first_partial_seconds",
                "Speech onset to the first transcript event.",
                self.first_partial,
            )
            histogram(
                "dictation_finalization_seconds",
                "Flush request to the final transcript event.",
                self.finalization,
            )
            histogram(
                "dictation_real_time_factor",
                "Decode wall-clock divided by audio duration.",
                self.real_time_factor,
            )

            lines.append("# HELP dictation_errors_total Errors sent to clients, by code.")
            lines.append("# TYPE dictation_errors_total counter")
            for code, count in sorted(self.errors_by_code.items()):
                lines.append(f'dictation_errors_total{{{labels},code="{code}"}} {count}')

            return "\n".join(lines) + "\n"

    def summary(self) -> dict[str, object]:
        """Compact snapshot for the `status` management command."""
        with self._lock:
            return {
                "sessions_active": self.sessions_active,
                "sessions_accepted": self.sessions_accepted,
                "sessions_rejected": self.sessions_rejected,
                "utterances": self.utterances_total,
                "decodes": self.decodes_total,
                "first_partial_p95": self.first_partial.quantile(0.95),
                "finalization_p95": self.finalization.quantile(0.95),
                "rtf_p95": self.real_time_factor.quantile(0.95),
                "errors": dict(self.errors_by_code),
            }
