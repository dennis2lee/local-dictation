# Latency: why partials are slow, and what fixes it

The project plan names CPU decode latency as its largest technical risk. It is
real, and the cause is not what a first guess suggests.

## The measurement

Decode time barely depends on how much audio you give it:

| audio in one pass | `large-v3-turbo` INT8 | `base` INT8 |
| ----------------- | --------------------- | ----------- |
| 3 s               | 3.09 s                | 0.25 s      |
| 6 s               | 3.30 s                | 0.30 s      |
| 10 s              | 3.45 s                | 0.35 s      |

<sub>Apple Silicon, 10 cores / 4 performance cores, `cpu_threads: 8`,
`beam_size: 1`, greedy, `word_timestamps: true`.</sub>

Whisper's encoder always processes a mel spectrogram padded to 30 seconds. Three
seconds of speech costs the same as ten. So the per-pass cost is essentially
fixed, and the thing that decides how quickly partial text appears is *how
expensive one pass is* — not how cleverly the audio is chunked.

Things that do **not** help, measured on the same clip:

- `chunk_length=10` — no change; CTranslate2 still pads the encoder. At
  `chunk_length=5` it gets *worse* (7.0 s), because six seconds of audio then
  needs two encoder passes.
- `word_timestamps=false` — no measurable saving, so they stay on. They are what
  lets the streaming layer trim decoded audio out of the buffer.
- `vad_filter=false` — no saving.

With one model, the floor on first-partial latency is
`chunk_ms + one decode`, which is about **3.7 s** here. The plan's target is
2 s. No amount of tuning closes that gap.

## The fix: a draft model

`model.draft_path` points at a second, much smaller conversion. Partial text
comes from it; the accurate model decodes the utterance once, at the end, and
that is the only text that is ever committed.

Measured end to end, streaming a real recording at real time through the
WebSocket protocol:

| | one model | draft `base` + `large-v3-turbo` |
| --- | --- | --- |
| First partial (English, 21 s clip) | 3.69 s | **0.88 s** |
| First partial (Korean, 10 s clip) | 3.76 s | **0.89 s** |
| Partials shown (English) | 7 | **33** |
| Finalization after stop | 3.87 s | 3.77 s |
| Final text | *identical in both* | *identical in both* |

The final text is identical because the draft never writes anything permanent.
It is a preview: it appears as composing text at the cursor, it gets replaced as
it is refined, and when the sentence ends the accurate model's reading is what
gets committed.

Turn it on:

```yaml
model:
  path: "/opt/local-dictation/models/large-v3-turbo"
  draft_path: "/opt/local-dictation/models/base"
```

```bash
./scripts/fetch-model.sh large-v3 --repo Systran/faster-whisper-base --dest /opt/local-dictation/models
mv /opt/local-dictation/models/large-v3 /opt/local-dictation/models/base
```

`base` is 145 MB and adds roughly 200 MB of resident memory.

In standalone mode the same thing is one field in the app: put the small
model's directory in **Settings → Local server → Draft model directory** and
save. The client passes it through to the server it starts, and **Test
connections** confirms it with "drafting with …".

One caveat for very long utterances: the decode window is capped (12 s by
default, `streaming.max_window_seconds`). When one utterance outgrows it, the
accurate model decodes the window once mid-utterance so that the text scrolled
out of the window is committed from *its* reading, never the draft's. That pass
costs one extra accurate decode per window length of continuous speech.

### Choosing the draft model

The draft only has to be good enough to *look* right while someone is speaking.
On English, `base` tracks the accurate model closely. On Korean it is noticeably
rougher — still useful as a preview, but if the flicker is distracting,
`Systran/faster-whisper-small` (~460 MB) is the next step up and still roughly
five times cheaper than turbo. Nothing about that choice affects the committed
text.

## Finalization

Finalization is one pass of the accurate model, so it inherits the same fixed
cost: about 3.8 s here against a 1.5 s target. There is no trick for it — it is
the price of the accuracy the plan asked for. The options are the honest ones:

- accept it (the user has stopped talking; the wait is at a natural pause),
- run the final pass on `large-v3-turbo` rather than `large-v3` — already the
  recommendation in [model-setup.md](model-setup.md), and roughly half the cost,
- or put the servers on faster cores.

Whatever you choose, measure it rather than assuming:

```bash
local-dictation-server health all
curl -s http://127.0.0.1:8765/status | python3 -m json.tool
```

`first_partial_p95`, `finalization_p95` and `rtf_p95` are reported there, and a
`null` means "above the largest bucket" — which is its own answer.

## Why the decode window is trimmed anyway

Whisper re-reads its whole input on every pass, and the fixed cost above only
holds up to 30 seconds. Past that the encoder runs multiple windows and the cost
really does grow with the length of the utterance. So once `max_window_seconds`
of audio has accumulated and there is committed text to anchor on, the streaming
layer drops the already-transcribed audio and carries its text forward as a
prompt. That keeps a five-minute monologue costing the same per pass as a
five-second sentence.
