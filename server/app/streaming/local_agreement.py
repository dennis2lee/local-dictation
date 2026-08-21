"""LocalAgreement-n stable prefix policy.

Whisper rewrites its own output as more audio arrives: "세" becomes "세 시",
"three" becomes "three thirty". Committing every hypothesis directly to the
user's cursor would make the text flicker and, worse, leave stale words behind
once a rewrite shortens the sentence.

LocalAgreement-n only commits what the last *n* consecutive hypotheses agree on.
With n = 2 that is the longest common prefix of the current and previous
hypothesis: text that survived one more pass of context. Everything after it
stays volatile and is rendered as composing text.

The prefix is computed over whitespace-delimited tokens, each carrying its own
trailing whitespace, so a committed prefix always ends on a word boundary and
`stable + partial` reconstructs the hypothesis byte for byte.
"""

from __future__ import annotations

import re
from collections import deque
from dataclasses import dataclass

_TOKEN = re.compile(r"\S+\s*")


def tokenize(text: str) -> list[str]:
    """Split into tokens that each keep their trailing whitespace.

    Leading whitespace, if any, is folded into the first token so that
    ``"".join(tokenize(t)) == t`` for every ``t``.
    """
    tokens = _TOKEN.findall(text)
    if not tokens:
        return [text] if text else []
    consumed = "".join(tokens)
    if len(consumed) != len(text):
        # Only possible when the text starts with whitespace.
        tokens[0] = text[: len(text) - len(consumed)] + tokens[0]
    return tokens


def common_prefix(a: list[str], b: list[str]) -> list[str]:
    limit = min(len(a), len(b))
    i = 0
    while i < limit and a[i] == b[i]:
        i += 1
    return a[:i]


def _words(tokens: list[str]) -> list[str]:
    """Tokens without their trailing whitespace.

    Comparisons for *identity* use this; comparisons for *agreement* use the raw
    tokens. A word that is last in one hypothesis and mid-sentence in the next
    is the same word (so not a conflict) but has not stopped growing (so not yet
    committable).
    """
    return [t.rstrip() for t in tokens]


@dataclass(frozen=True)
class AgreementResult:
    #: The committed prefix after this update. Never shorter than before.
    stable: str
    #: Whatever the hypothesis has beyond `stable`. `stable + partial` is the
    #: hypothesis exactly, unless `conflict` is set.
    partial: str
    #: True when the hypothesis contradicts already-committed text. The caller
    #: must start a new utterance rather than retract what the user can see.
    conflict: bool


class LocalAgreement:
    def __init__(self, window: int = 2) -> None:
        if window < 2:
            raise ValueError("agreement window must be at least 2")
        self._window = window
        # The previous window-1 hypotheses, as token lists.
        self._history: deque[list[str]] = deque(maxlen=window - 1)
        self._committed: list[str] = []

    @property
    def committed(self) -> str:
        return "".join(self._committed)

    @property
    def window(self) -> int:
        return self._window

    def reset(self, committed: str = "") -> None:
        self._history.clear()
        self._committed = tokenize(committed)

    def update(self, hypothesis: str) -> AgreementResult:
        """Fold one fresh hypothesis in and report what is now committable."""
        tokens = tokenize(hypothesis)
        committed_len = len(self._committed)

        if _words(tokens[:committed_len]) != _words(self._committed):
            # The decoder revised text the user has already seen committed. We
            # cannot take it back, so tell the caller to rotate the utterance.
            self._history.clear()
            self._history.append(tokens)
            return AgreementResult(stable=self.committed, partial="", conflict=True)

        # Re-anchor the committed tokens onto this hypothesis so that `stable`
        # stays a literal prefix of it even if the decoder re-spaced a word.
        self._committed = tokens[:committed_len]

        # LocalAgreement-n needs n hypotheses before anything can be committed;
        # until the history is full, everything stays partial.
        if len(self._history) >= self._window - 1:
            agreed = tokens
            for previous in self._history:
                agreed = common_prefix(agreed, previous)
                if len(agreed) <= committed_len:
                    break
            # A token with no trailing whitespace is the last word of the
            # hypothesis, and more audio may still extend it ("세" -> "세시").
            # Holding it back costs one pass and avoids an uncommittable edit.
            while agreed and not agreed[-1][-1:].isspace():
                agreed.pop()
            if len(agreed) > committed_len:
                self._committed = agreed

        self._history.append(tokens)

        stable = self.committed
        return AgreementResult(
            stable=stable,
            partial=hypothesis[len(stable):],
            conflict=False,
        )

    def commit_all(self, hypothesis: str) -> AgreementResult:
        """Commit the whole hypothesis, as done when an utterance ends.

        Reports a conflict — without mutating state — if the hypothesis does not
        extend what is already committed.
        """
        tokens = tokenize(hypothesis)
        if _words(tokens[: len(self._committed)]) != _words(self._committed):
            return AgreementResult(stable=self.committed, partial="", conflict=True)
        self._committed = tokens
        self._history.clear()
        return AgreementResult(stable="".join(tokens), partial="", conflict=False)
