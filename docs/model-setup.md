# Installing a Whisper model

Neither the server package nor the client installers contain a model. They are
several gigabytes, they carry their own licence, and every site mirrors them
differently. Installing one is a single command.

**Using the desktop client?** **Settings → Models** does everything on this page
except the offline transfer: it lists what is installed, marks what is missing,
and fetches it. This page is for a server you run yourself, for an air-gapped
install, or for anyone who would rather see the command.

The same Whisper weights come in different formats, and which one you want
depends on how the server will decode. Most of this page is about the
**CTranslate2** conversions, which is what `faster-whisper` loads and what every
platform can run. There are two GPU alternatives, each the only route to the
GPU on its hardware: **MLX** on an Apple Silicon Mac, and **OpenVINO** on an
Intel GPU. No backend reads another's files.

Whichever you pick, it is a local directory. The plain `openai/whisper-*`
repositories are PyTorch checkpoints and will not work with any of them.

## Which model

Several things are downloadable, and a normal install needs one or two of them.

| | What it is | Size | Setting |
| --- | --- | --- | --- |
| `large-v3-turbo` | The accurate model. What the shipped configs name. | 1.5 GiB | `model.path` |
| `large-v3` | More accurate, several times slower. | 2.9 GiB | `model.path` |
| `base` | Draft model — live partial text only, never committed. | 140 MiB | `model.draft_path` |
| `large-v3-turbo-mlx` | `large-v3-turbo` again, for the Apple Silicon GPU. | 1.5 GiB | `model.path`, with `--backend mlx` |
| `large-v3-turbo-openvino-int8` | `large-v3-turbo` again, for an Intel GPU. | 790 MiB | `model.path`, with `--backend openvino` |

`large-v3-turbo-openvino-fp16` and `-int4` are the same model at other
precisions — see [`large-v3-turbo-openvino-*`](#large-v3-turbo-openvino-on-an-intel-gpu).

**Start here:**

- **On Linux or Windows with no usable GPU** — `large-v3-turbo`, then `base`
  alongside it. That second one is small and it is the difference between text
  appearing in five seconds and in one.
- **On an Apple Silicon Mac** — `large-v3-turbo-mlx` on its own. The GPU is fast
  enough that no draft model is needed.
- **On a machine with an Intel GPU** — `large-v3-turbo-openvino-int8` on its
  own, and measure before adding a draft model. See below.

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

The three conversions are not interchangeable: `model.bin` is the CTranslate2
one, `weights.safetensors` the MLX one and `openvino_encoder_model.xml` the
OpenVINO one. Each backend refuses the others' directories at startup, and
`check` catches it before a restart.

### `large-v3-turbo-openvino-*`, on an Intel GPU

The same weights a third time, exported to OpenVINO IR. This is the only way to
reach an Intel GPU — an Arc 140V in a Lunar Lake laptop, an Arc card, an older
Iris Xe — because CTranslate2 has no Intel GPU backend either.

Three precisions are published, and **which one to install is a measurement,
not a preference**:

| | Size | What it is for |
| --- | --- | --- |
| `large-v3-turbo-openvino-int8` | 790 MiB | The expected answer. Start here. |
| `large-v3-turbo-openvino-fp16` | 1.6 GiB | The accuracy reference to compare against. |
| `large-v3-turbo-openvino-int4` | 600 MiB | Smallest and fastest, and the one most likely to cost Korean accuracy. |

Quantisation costs accuracy in a way only real speech shows, and in a Korean or
mixed Korean/English transcript it shows before it shows in English. So install
at least INT8 and FP16, and compare them on your own clips before settling:

```bash
./scripts/openvino-benchmark.py --models "$PREFIX/models" --device GPU
```

That reports decode time and real-time factor per precision, and nothing about
accuracy — a real-time factor under 0.25 is the target. For accuracy, run
[`benchmark.py`](server-usage.md) with a manifest of Korean clips against a
server on each precision and read CER. If INT4 is meaningfully worse, it is not
the one to ship, however fast it is.

**No draft model to begin with.** On CPU the `base` draft is what brings the
first partial under a second, but it exists to hide a 3.2 s decode. If the GPU
decodes fast enough there is nothing to hide, and one model instead of two is
less memory, a shorter start and no disagreement between the partial text and
the committed text. Add a draft only if the measurement says to — and if it
does, it needs to be an OpenVINO export as well, because the draft runs on the
same backend as the accurate model.

Then point `model.path` at the export, set `model.device` to `GPU`, and start
the server with `--backend openvino`. In the client that is **Settings → Local → Decode on → Intel GPU**.

**It will not quietly fall back to the CPU.** A machine with no Intel GPU, a
missing graphics driver or a missing OpenVINO GPU plugin is a startup error
naming the devices OpenVINO did find. That is deliberate: a GPU backend
silently decoding on the CPU looks exactly like a slow computer.

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
it is the difference between text appearing in five seconds and in one.

**On a machine with an Intel GPU, fetch the OpenVINO export instead** — and
instead is the word, because the two are different formats and the Intel GPU
backend cannot read the one above:

```powershell
.\fetch-model.ps1 -Model large-v3-turbo-openvino-int8
```

Then set **Settings → Local → Decode on** to **Intel GPU** and point
**Model directory** at the directory it just wrote. Leaving the directory
pointing at `large-v3-turbo` is the mistake to avoid; the client says so as soon
as the backend is chosen, rather than waiting for the first attempt to dictate.

There is no MLX option here — that is Apple Silicon only.

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
`Systran/faster-whisper-base`, the MLX conversion from
`mlx-community/whisper-large-v3-turbo`, and the OpenVINO exports from
`OpenVINO/whisper-large-v3-turbo-{int8,fp16,int4}-ov`. Silero VAD is MIT
licensed. Check
each repository's card before redistributing anything internally.
