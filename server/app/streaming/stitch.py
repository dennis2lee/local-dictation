"""Joining a fresh decode onto the text already committed before it.

The streaming layer drops audio whose text is committed and hands that text
back to the decoder as a prompt, so the decode of a trimmed window still knows
what sentence it is in the middle of. Whisper's contract for that prompt is
that it is the text *preceding* the audio — but nothing stops the model from
carrying the prompt into its own output, and it does, routinely and at length.

Measured on this project's large-v3-turbo conversions, same audio, same call,
prompt the only difference:

    audio from 0.18s  prompt=''      -> '회의에서는 지난 분기 실적과 …'
    audio from 0.18s  prompt='오늘 '  -> '오늘 회의에서는 지난 분기 실적과 …'

The word "오늘" is not in that audio; it was trimmed away because it had
already been committed and typed. The session then prepends the committed text
to the hypothesis, as it must, and the user gets "오늘 오늘 회의에서는". Give
the model a longer prompt and it repeats more of it — 0.45 s of audio and a
one-sentence prompt came back as the whole sentence.

So the join has to assume the hypothesis may begin by saying what the caller
already has, and cut that off. The same cut covers a second case with the same
shape: a trim landing a fraction of a second inside a word leaves its tail in
the window, and the next pass transcribes those syllables again.

What is deliberately *not* done here is trusting the text to line up
character for character. Whisper re-spaces Korean between passes — the same
clip came back as both "제출해주세요" and "제출해 주세요" — so comparison
ignores whitespace, ignores case for the languages that have one, and ignores
U+FFFD, which is what a hypothesis cut mid-character decodes to.
"""

from __future__ import annotations

#: Never cut more than this from the front of a hypothesis. The model can only
#: repeat what it was shown, and the session shows it at most this much, so a
#: longer match is a coincidence rather than a repeat.
MAX_REPEAT_CHARACTERS = 240

#: Shortest overlap treated as a repeat rather than a coincidence.
#:
#: One character is too short to act on: a committed "…했습니다. " and a
#: hypothesis opening "다음 주에…" share their "다", and cutting it would
#: leave the user reading "음 주에". Two is enough to be worth acting on and
#: rare enough to be safe — the cost of being wrong is one clipped word, the
#: cost of doing nothing is a whole sentence typed twice.
MIN_REPEAT_CHARACTERS = 2

#: Whisper's byte-level BPE can end a hypothesis inside a multi-byte character,
#: which decodes to this. It is noise at a join, never content.
REPLACEMENT_CHARACTER = "�"


def _comparable(text: str) -> tuple[str, list[int]]:
    """The form overlaps are matched on, plus where each character came from.

    The map is what turns a match length back into a slice of the original: it
    holds, for every character kept, its index in `text`.
    """
    kept: list[str] = []
    positions: list[int] = []
    for index, character in enumerate(text):
        if character.isspace() or character == REPLACEMENT_CHARACTER:
            continue
        kept.append(character.casefold())
        positions.append(index)
    return "".join(kept), positions


def repeated_prefix(previous: str, hypothesis: str) -> int:
    """How many characters of `hypothesis` merely repeat the tail of `previous`.

    Returns a count of characters in `hypothesis`, ready to slice off, or 0
    when the two do not overlap. The longest overlap wins: the failure being
    corrected repeats a whole prompt, not a syllable of one.
    """
    if not previous or not hypothesis:
        return 0

    tail, _ = _comparable(previous)
    head, positions = _comparable(hypothesis)
    if not tail or not head:
        return 0

    limit = min(len(tail), len(head), MAX_REPEAT_CHARACTERS)
    for length in range(limit, MIN_REPEAT_CHARACTERS - 1, -1):
        if tail[-length:] != head[:length]:
            continue
        if length == len(positions):
            return len(hypothesis)
        # Cut up to the next character that survived comparison, so the
        # whitespace between the repeat and the new text goes with it.
        return positions[length]
    return 0


def drop_repeated_prefix(previous: str, hypothesis: str) -> str:
    """`hypothesis` with any repeat of `previous`'s tail removed."""
    return hypothesis[repeated_prefix(previous, hypothesis) :].lstrip()


__all__ = [
    "MAX_REPEAT_CHARACTERS",
    "MIN_REPEAT_CHARACTERS",
    "drop_repeated_prefix",
    "repeated_prefix",
]
