# Installing a Whisper model

Neither the server package nor the client installers contain a model. They are
several gigabytes, they carry their own licence, and every site mirrors them
differently. Installing one is a single command.

The same Whisper weights come in different formats, and which one you want
depends on how the server will decode. Most of this page is about the
**CTranslate2** conversions, which is what `faster-whisper` loads and what every
platform can run. On an Apple Silicon Mac there is a second option, **MLX**,
which is the only route to the GPU. Neither backend reads the other's files.

Whichever you pick, it is a local directory. The plain `openai/whisper-*`
repositories are PyTorch checkpoints and will not work with either.

## Which model

Four things are downloadable, and a normal install needs one or two of them.

| | What it is | Size | Setting |
| --- | --- | --- | --- |
| `large-v3-turbo` | The accurate model. What the shipped configs name. | 1.5 GiB | `model.path` |
| `large-v3` | More accurate, several times slower. | 2.9 GiB | `model.path` |
| `base` | Draft model — live partial text only, never committed. | 140 MiB | `model.draft_path` |
| `large-v3-turbo-mlx` | `large-v3-turbo` again, for the Apple Silicon GPU. | 1.5 GiB | `model.path`, with `--backend mlx` |

**Start here:**

- **On Linux or Windows** — `large-v3-turbo`, then `base` alongside it. That
  second one is small and it is the difference between text appearing in five
  seconds and in one.
- **On an Apple Silicon Mac** — `large-v3-turbo-mlx` on its own. The GPU is fast
  enough that no draft model is needed.

Every download also fetches `silero_vad.onnx` (2.2 MiB), which is how the server
decides an utterance has ended.

### `large-v3` against `large-v3-turbo`

| | `large-v3` | `large-v3-turbo` |
|---|---|---|
| Download | 2.9 GiB | 1.5 GiB |
| Resident (INT8) | ~2 GB | ~1.2 GB |
| Decoder layers | 32 | 4 |
| Relative CPU speed | baseline | several times faster |
| Accuracy | highest | slightly lower |

`large-v3` is what the project plan specifies. **The shipped configs point at
`large-v3-turbo` anyway** — the plan's largest technical risk is exactly that
`large-v3` cannot hold the first-partial latency budget on CPU, and turbo is the
mitigation that does not require new hardware. Turbo cuts the decoder from 32
layers to 4, so it loses some accuracy; how much depends on the language and the
audio, and OpenAI has noted the loss is uneven across languages. Measure both on
your own recordings before choosing:

```bash
local-dictation-server health all      # first_partial_p95 and rtf_p95 live here
```

Keep the real-time factor (`rtf_p95`) under 1.0. Above it, the decoder is falling
behind the microphone and no amount of tuning elsewhere will fix the latency.

### `base`, the draft model

140 MB, and on CPU it is worth more than the choice above.

Whisper's encoder processes a padded 30-second window whatever you give it, so
a decode pass costs the same for three seconds of speech as for ten. That fixed
cost is the floor on how quickly partial text can appear, and for
`large-v3-turbo` on a laptop it is several seconds — well past the two the plan
budgets. `base` runs the same pass in a fraction of it.

Set as `model.draft_path`, it writes **only the live partial text**. Nothing it
produces is committed: when you stop, the accurate model decodes the utterance
once and that is the text you keep. So this buys latency without spending
accuracy, which almost nothing else here does.

Measured on a MacBook Air M5, Korean, five clips:

| | first partial p50 | finalization p50 | CER |
|---|---|---|---|
| `large-v3-turbo` alone | 4.85 s | 7.47 s | 5.3% |
| with `base` as the draft | **0.92 s** | **4.58 s** | 5.3% |

Identical accuracy, first partial five times sooner.

### `large-v3-turbo-mlx`, on an Apple Silicon Mac

The same weights again, converted for MLX. This is the only way to reach the GPU
on Apple Silicon: CTranslate2 has no Metal backend, so no value of
`model.device` gets you there.

| | first partial p50 | finalization p50 | CER |
|---|---|---|---|
| `large-v3-turbo` + `base` draft, CPU | 0.92 s | 4.58 s | 5.3% |
| **`large-v3-turbo-mlx`, no draft** | **1.29 s** | **1.12 s** | 5.3% |

Same accuracy again — same model. It is the only configuration that meets both
latency targets, and it needs one model rather than two to do it. It also runs at
a real-time factor of 0.12 instead of 0.77, which matters on a fanless machine
where sustained load is what makes it throttle.

It needs its own backend, and one extra package:

```bash
"$PREFIX/venv/bin/python" -m pip install mlx-whisper
```

Then point `model.path` at the MLX directory, clear `model.draft_path`, and
start the server with `LD_BACKEND=mlx`. See
[Choosing a backend](server-usage.md#choosing-a-backend).

The two conversions are not interchangeable: `model.bin` is the CTranslate2 one
and `weights.safetensors` the MLX one. Each backend refuses the other's
directory at startup, and `check` catches it before a restart.

## Linux and macOS

`$PREFIX` below is wherever you installed the server: `~/local-dictation` by
default, `/opt/local-dictation` for a `sudo` install, or whatever you passed to
`--prefix`.

```bash
cd "$PREFIX/app"   # or the server/ directory of your checkout
```

```bash
./scripts/fetch-model.sh --list
```

Then, on Linux or an Intel Mac — the accurate model and the draft one:

```bash
./scripts/fetch-model.sh large-v3-turbo --dest "$PREFIX/models"
```

```bash
./scripts/fetch-model.sh base --dest "$PREFIX/models"
```

Or on an Apple Silicon Mac, one command instead of those two:

```bash
./scripts/fetch-model.sh large-v3-turbo-mlx --dest "$PREFIX/models"
```

Every one of them also fetches `silero_vad.onnx`. `all` fetches the three
CTranslate2 models and the VAD — not the MLX one, which would be 1.5 GB that a
Linux server can never load:

```bash
./scripts/fetch-model.sh all --dest "$PREFIX/models"
```

The configs already name `$PREFIX/models/large-v3-turbo`, so fetching that one
needs no further edit. The other three are settings you add.

## Windows

The standalone client needs a model on the machine it runs on. From the
installed `server\scripts` directory:

```powershell
.\fetch-model.ps1 -Model large-v3-turbo
```

```powershell
.\fetch-model.ps1 -Model base
```

The second one is the draft model, and on a Windows laptop decoding on the CPU
it is the difference between text appearing in five seconds and in one. There is
no MLX option here — that is Apple Silicon only.

The default destination is `%LOCALAPPDATA%\LocalDictation\models`, which is where
the client looks in standalone mode.

## Closed network

Run the download on a machine that has internet, carry the directory across, and
re-verify on the far side. The checksum manifest is written at download time, so
the verification proves the transfer, not just the download.

```bash
./scripts/fetch-model.sh all --dest ./ld-models
tar czf ld-models.tgz ld-models
```

```bash
scp ld-models.tgz dictation-server:/tmp/
ssh dictation-server 'tar xzf /tmp/ld-models.tgz -C /opt/local-dictation --strip-components=1'
ssh dictation-server '/opt/local-dictation/app/scripts/fetch-model.sh --verify'
```

If your site mirrors HuggingFace internally, point the script at the mirror
instead of copying files by hand:

```bash
HF_ENDPOINT=https://models.internal ./scripts/fetch-model.sh large-v3
```

## Wiring it into the config

Both language servers share one model directory — there is no Korean model and
English model, only a Korean *process* and an English *process*.

On a CPU, with the draft model:

```yaml
model:
  path: "<prefix>/models/large-v3-turbo"
  draft_path: "<prefix>/models/base"
streaming:
  silero_model_path: "<prefix>/models/silero_vad.onnx"
```

On an Apple Silicon GPU, started with `LD_BACKEND=mlx`:

```yaml
model:
  path: "<prefix>/models/large-v3-turbo-mlx"
  draft_path: null
streaming:
  silero_model_path: "<prefix>/models/silero_vad.onnx"
```

The installer writes `model.path` and `silero_model_path` for the prefix you
chose, so those are what to check rather than what to type. `draft_path` starts
empty and is yours to add. Apply any change to both files and restart:

```bash
local-dictation-server restart all && local-dictation-server health all
```

The server refuses to start against a missing directory and never reaches for
the internet to fill one in — on a closed network a silent download attempt
would hang until the first utterance timed out, which is far harder to diagnose
than a startup error.

## Verifying an existing install

```bash
./scripts/fetch-model.sh --verify --dest "$PREFIX/models"
```

```powershell
.\fetch-model.ps1 -Verify -Dest D:\models
```

A file that fails here is corrupt or truncated; re-run the download with
`--force` (`-Force`) to replace it.

## Licence

Whisper weights are released by OpenAI under the MIT licence. The CTranslate2
conversions used here are redistributions of those weights: `large-v3` from
`Systran/faster-whisper-large-v3`, `large-v3-turbo` from
`deepdml/faster-whisper-large-v3-turbo-ct2`, `base` from
`Systran/faster-whisper-base`, and the MLX conversion from
`mlx-community/whisper-large-v3-turbo`. Silero VAD is MIT licensed. Check
each repository's card before redistributing anything internally.
