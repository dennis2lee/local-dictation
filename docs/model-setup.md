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

`large-v3` is what the project plan specifies and what the shipped configs point
at. **On CPU, start with `large-v3-turbo` anyway** — the plan's largest technical
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

## Linux and macOS

```bash
cd /opt/local-dictation/app/server   # or your checkout
./scripts/fetch-model.sh --list
```

Then pick one:

```bash
./scripts/fetch-model.sh large-v3-turbo --dest /opt/local-dictation/models
```

```bash
./scripts/fetch-model.sh large-v3 --dest /opt/local-dictation/models
```

Both commands also fetch `silero_vad.onnx` (2.2 MiB), which the server uses to
decide when an utterance has ended. To install both models at once:

```bash
./scripts/fetch-model.sh all --dest /opt/local-dictation/models
```

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
ssh dictation-server '/opt/local-dictation/app/server/scripts/fetch-model.sh --verify'
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
  path: "/opt/local-dictation/models/large-v3-turbo"
streaming:
  silero_model_path: "/opt/local-dictation/models/silero_vad.onnx"
```

Apply it to both files and restart:

```bash
local-dictation-server restart all && local-dictation-server health all
```

The server refuses to start against a missing directory and never reaches for
the internet to fill one in — on a closed network a silent download attempt
would hang until the first utterance timed out, which is far harder to diagnose
than a startup error.

## Verifying an existing install

```bash
./scripts/fetch-model.sh --verify --dest /opt/local-dictation/models
```

```powershell
.\fetch-model.ps1 -Verify -Dest D:\models
```

A file that fails here is corrupt or truncated; re-run the download with
`--force` (`-Force`) to replace it.

## Licence

Whisper weights are released by OpenAI under the MIT licence. The CTranslate2
conversions used here are redistributions of those weights: `large-v3` from
`Systran/faster-whisper-large-v3` and `large-v3-turbo` from
`deepdml/faster-whisper-large-v3-turbo-ct2`. Silero VAD is MIT licensed. Check
each repository's card before redistributing anything internally.
