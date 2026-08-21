"""Logging and metrics.

The one rule that shapes both: no audio, no transcript text, ever. Metrics are
timings and counts; logs carry session ids, error codes and versions. The tests
in tests/test_privacy.py assert it rather than trusting it.
"""

from app.observability.logging import configure_logging
from app.observability.metrics import Metrics

__all__ = ["Metrics", "configure_logging"]
