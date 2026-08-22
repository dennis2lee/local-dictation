# Installing a Whisper model

Neither the server package nor the client installers contain a model. They are
several gigabytes, they carry their own licence, and every site mirrors them
differently. Installing one is a single command.

Everything below downloads a **CTranslate2 conversion**, which is the format
`faster-whisper` loads directly. The plain `openai/whisper-*` repositories are
PyTorch checkpoints and will not work.

## Which model

| |`large-v3`|`large-v3-turbo`|
|---|---|---|
| Download | 2.9 GiB | 1.5 GiB |
| Resident (INT8) | ~2 GB | ~1.2 GB |
| Decoder layers | 32 | 4 |
| Relative CPU speed | baseline | several times faster |
| Accuracy | highest | slightly lower |

`large-v3` is what the project plan specifies. **The shipped configs point at
`large-v3-turbo` anyway** — the plan's largest technical
risk is exactly that `large-v3` cannot hold the first-partial latency budget on
CPU, and turbo is the mitigation that does not require new hardware. Turbo cuts
the decoder from 32 layers to 4, so it loses some accuracy; how much depends on
the language and the audio, and OpenAI has noted the loss is uneven across
languages. Measure both on your own recordings before choosing:

```bash
local-dictation-server health all      # first_partial_p95 and rtf_p95 live here
```

Keep the real-time factor (`rtf_p95`) under 1.0. Above it, the decoder is falling
behind the microphone and no amount of tuning elsewhere will fix the latency.

### And one more: `base`, the draft model

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

Identical accuracy, first partial five times sooner. Get it with
`./fetch-model.sh base`, and see `model.draft_path` in
[server-usage.md](server-usage.md#every-setting).

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

Then pick one:

```bash
./scripts/fetch-model.sh large-v3-turbo --dest "$PREFIX/models"
```

```bash
./scripts/fetch-model.sh large-v3 --dest "$PREFIX/models"
```

Both commands also fetch `silero_vad.onnx` (2.2 MiB), which the server uses to
decide when an utterance has ended. To install both models at once:

```bash
./scripts/fetch-model.sh all --dest "$PREFIX/models"
```

The configs already name `$PREFIX/models/large-v3-turbo`, so fetching that one
needs no further edit.

## Windows

The standalone client needs a model on the machine it runs on. From the
installed `server\scripts` directory:

```powershell
.\fetch-model.ps1 -Model large-v3-turbo
```

```powershell
.\fetch-model.ps1 -Model large-v3 -Dest D:\models
```

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

```yaml
model:
  path: "<prefix>/models/large-v3-turbo"
  draft_path: "<prefix>/models/base"      # optional, and the biggest win on CPU
streaming:
  silero_model_path: "<prefix>/models/silero_vad.onnx"
```

The installer writes the first and last of these for the prefix you chose, so
those are what to check rather than what to type. `draft_path` starts empty and
is yours to add. Apply any change to both files and restart:

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
`deepdml/faster-whisper-large-v3-turbo-ct2`, and `base` from
`Systran/faster-whisper-base`. Silero VAD is MIT licensed. Check
each repository's card before redistributing anything internally.
