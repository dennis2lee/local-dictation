# Running the servers

For whoever runs the speech servers day to day: what the commands do, what to
configure, what to watch, and how to measure it. Getting one installed in the
first place is [server-install.md](server-install.md).

If you use standalone mode you do not need this file — the client manages its
own server.

None of it needs root. An install into a prefix you own leaves every command
below yours to run.

## The shape of it

Two independent processes, one per language, deliberately not one process
serving both:

| Service | Language | Default port |
| --- | --- | --- |
| `local-dictation-ko` | Korean | 8765 |
| `local-dictation-en` | English | 8766 |

They share a model directory and a codebase, and differ only in the `language`
and `port` in their config. Restarting Korean must never interrupt an English
session, which is exactly what separate processes buy.

## Everyday commands

```bash
local-dictation-server start all
local-dictation-server status all
local-dictation-server health all
local-dictation-server version
local-dictation-server logs ko --follow
local-dictation-server restart en
local-dictation-server stop all
```

`status` asks the operating system whether a process is alive. `health` asks the
server whether it can actually transcribe — a process can be running while the
model is still loading, and that difference is usually what you are checking.
`version` asks a third thing: whether what is running is what is installed.

```
installed  0.1.3  (/home/you/local-dictation)
ko         0.1.3  running
en         0.1.2  running — restart it to pick up 0.1.3
```

`check` validates both config files and opens every file they name — the model,
the VAD model, the certificates — without binding a port. Run it before a
restart: a model directory that is not there is a message here rather than a
server that exits four seconds after `start` reported it as up.

To check a config file on a machine that is not the one that will serve it —
editing on a laptop, or in CI — ask the narrower question directly, which
validates the file without requiring anything it names to be installed:

```
python -m app.main --config config/server-ko.yaml --check-config
```

`start` waits up to `LD_START_TIMEOUT` seconds (default 15) for the new process
to either fail or bind its port, so a start that cannot work is reported as a
failure with the reason from the log. Loading a model takes minutes and happens
before the bind, so a real start usually reaches that timeout and says it is
still loading; `health` is what answers when it can transcribe.

## Where things live

The management script finds everything through `LD_*`, which is how one script
serves both a system install and a checkout.

The `local-dictation-server` command you normally run is a wrapper the installer
writes, and it **pins** the six path variables to its own prefix. That command is
that install, so an `LD_PYTHON` left exported in a shell must not be able to
redirect it — it says it is ignoring one rather than obeying it. `LD_BACKEND`
and `LD_START_TIMEOUT` are not paths and still come from the environment.

To point at a different tree, run `<prefix>/app/scripts/local-dictation-server`
directly. That is the unwrapped script, and it reads every variable below.

| Variable | Default | What it names |
| --- | --- | --- |
| `LD_HOME` | `/opt/local-dictation` | The install root the rest default to |
| `LD_APP_DIR` | `$LD_HOME/app` | The directory *containing* `app/` |
| `LD_CONFIG_DIR` | `$LD_HOME/config` | `server-ko.yaml` and `server-en.yaml` |
| `LD_PYTHON` | `$LD_HOME/venv/bin/python` | The interpreter |
| `LD_RUN_DIR` | `$LD_HOME/run` | PID files; must be writable by you |
| `LD_LOG_DIR` | `$LD_HOME/log` | Log files; must be writable by you |
| `LD_BACKEND` | `whisper` | `mlx` for an Apple Silicon GPU, `fake` starts without a model, for plumbing checks |
| `LD_START_TIMEOUT` | `15` | Seconds `start` watches for an early exit; `0` not to wait |
| `LD_RELEASE_REPO` | `dennis2lee/local-dictation` | Where `update` with no argument looks for a release |

`start` and `check` first confirm that `LD_PYTHON` can import what the server
needs. An interpreter without them produces a traceback out of `app/main.py`
that says nothing about what to do, so they say it instead: which modules are
missing, and that re-running the installer over this prefix puts them there.

## Configuration

One YAML file per language, in `$LD_CONFIG_DIR`. Changing any setting is the
same three steps — edit the file, check it, restart that language:

```bash
$EDITOR ~/local-dictation/config/server-ko.yaml
```

```bash
local-dictation-server check ko && local-dictation-server restart ko
```

Both files, and `all`, when the setting should apply to both. An update never
overwrites a config you have edited, so these survive upgrades.

Every setting can also be overridden by an environment variable, which is how a
host-specific value gets injected without editing a shipped file:

```bash
LOCAL_DICTATION_MODEL__PATH=/mnt/models/large-v3-turbo local-dictation-server restart all
```

The pattern is `LOCAL_DICTATION_<SECTION>__<KEY>`, upper-cased, with two
underscores and exactly two levels. The sections are `SERVER`, `MODEL`,
`STREAMING`, `SECURITY`, `LIMITS` and `LOGGING`. A key that does not exist in a
real section is an error at startup rather than a silently ignored typo — which
matters most for `logging.store_audio`, where a misspelling would look like it
had been set. `LOCAL_DICTATION_CONFIG` names the config file itself, which is
what `--config` does on the command line.

Precedence runs command line, then environment, then YAML, then the defaults.

### Changing the ports

The port lives in each language's config, and the two must differ:

```yaml
# <prefix>/config/server-ko.yaml
server:
  port: 9765
```

```bash
local-dictation-server check ko && local-dictation-server restart ko
```

`check` does not bind the port, so it will not tell you the new one is free —
`start` will, by failing with `address already in use` and printing it.

For a one-off, or for a host-specific port that should not be written into a
shipped file, the environment override does the same thing without editing
anything:

```bash
LOCAL_DICTATION_SERVER__PORT=9765 local-dictation-server restart ko
```

Three things have to agree on the number, and only the first is in this file:

| Where | What to change |
| --- | --- |
| The server | `server.port` in that language's config |
| The firewall | The inbound rule, if the server is not on loopback |
| Each client | **Settings → Server**, the Korean and English port fields |

A client pointed at the old port reports that it cannot reach the server, which
is accurate and unhelpful — change the clients in the same sitting.

In standalone mode none of this applies: the client starts the server itself and
leaves the ports at `0`, which means "pick a free one at startup". Pin them in
**Settings → Advanced** only if something else on the machine needs those
numbers to be predictable.

### Changing how many sessions a server accepts

```yaml
# <prefix>/config/server-ko.yaml
limits:
  max_sessions: 1
```

`max_sessions` is a hard gate, not a queue: the session over the limit is
refused with `server_busy` and the client shows a recoverable error. That is
deliberate, and [Capacity](#capacity) is why — a decoder already at its limit
does not get faster by being given more work.

**Raise it only after measuring.** `rtf_p95` on `/status` is decode wall-clock
over audio duration; it has to stay below 1.0 for one session before a second is
safe, because above 1.0 the decoder is falling behind the microphone and every
session misses the latency budget together. Eight performance cores hold one
`large-v3-turbo` session comfortably.

Lower it to 1 on a laptop that is also doing other work. Setting it to 0 is
rejected at startup.

The two language servers count separately: `max_sessions: 2` in each is four
concurrent sessions on the machine, which is usually not what someone means.

### Choosing a backend

Which engine decodes the audio is a launch choice, not a config key:
`--backend`, or `LD_BACKEND` for the management script. It is not in the YAML
because the right answer depends on the machine, and the same config file gets
copied between them.

Set it durably in **`<prefix>/config/environment`**, which the installed
command sources before every operation and which an upgrade never overwrites:

```bash
echo LD_BACKEND=mlx >> ~/local-dictation/config/environment
```

That file is the only durable home the choice has, and it matters more than it
sounds: a `model.path` holding MLX weights will not start under the default
backend, so a restart from a shell that happened not to export `LD_BACKEND`
would fail with a model it cannot read.

| | `whisper` (default) | `mlx` | `openvino` |
| --- | --- | --- | --- |
| Runs on | any CPU, and NVIDIA GPUs | Apple Silicon GPU | Intel GPU or NPU |
| Engine | faster-whisper / CTranslate2 | MLX | OpenVINO GenAI |
| Model format | `model.bin` | `weights.safetensors` | `openvino_encoder_model.xml` |
| Install | `pip install -e '.[inference]'` | `pip install -e '.[mlx]'` | `pip install -e '.[openvino]'` |
| Fetch the model | `fetch-model.sh large-v3-turbo` | `fetch-model.sh large-v3-turbo-mlx` | `fetch-model.sh large-v3-turbo-openvino-int8` |
| Also set | — | — | `model.device: GPU` |

The three conversions are not interchangeable, and each backend says so rather
than failing inside the first decode. `check` catches it before a restart.

**`openvino` needs `model.device` set as well**, and it will not choose for
you: `AUTO`, `HETERO` and `MULTI` are refused rather than accepted, because
each of them silently lands on the CPU when the GPU plugin fails to load, and
the only symptom of that is dictation being slow. Name `GPU` — or `GPU.1` on a
machine with a discrete card beside the integrated one — and a device that is
not there is a startup error listing the ones that are.

`/health/ready` and `/status` report what actually loaded:

```json
"engine": {"backend": "openvino", "device": "GPU", "device_name": "Intel(R) Arc(TM) 140V GPU"}
```

That is the only way to confirm from outside the process that a GPU is doing
the work. An open port proves nothing — every backend serves the same protocol
on it.

**On a Mac, `mlx` is worth reaching for.** Measured on a MacBook Air M5, Korean,
five clips through the real WebSocket protocol:

| | first partial p50 | finalization p50 | CER |
| --- | --- | --- | --- |
| `whisper`, CPU | 4.85 s | 7.47 s | 5.3% |
| `whisper`, CPU, with a `base` draft model | 0.92 s | 4.58 s | 5.3% |
| **`mlx`, no draft model** | **1.29 s** | **1.12 s** | **5.3%** |

The same accuracy in every row — it is the same model. The last one is the only
configuration that meets both latency targets, and it needs no draft model to
do it: on the GPU the accurate model is already fast enough to write the
partial text itself. Real-time factor goes from 0.77 to 0.12, which also
matters on a fanless machine, where sustained load is what makes it throttle.

`mlx` is macOS-and-arm64 only and its dependency is marked as such, so nothing
about a Linux or Windows install changes by its existence.

### The handful worth understanding first

Ten of the thirty-four, and the ones a real deployment usually touches. The
[full reference](#every-setting) is below.

| Setting | Why it matters |
| --- | --- |
| `model.path` | A local directory. The server never reaches the internet; a wrong path fails at startup instead of hanging on first use. |
| `model.draft_path` | Optional small model for live partial text. See [latency.md](latency.md); this is the single biggest latency change available. |
| `model.cpu_threads` | `0` lets CTranslate2 choose. Pin it to the physical core count after benchmarking; hyperthreads make INT8 slower. |
| `streaming.chunk_ms` | How often a decode pass runs. Lower is snappier and costs more CPU. |
| `streaming.silence_ms` | Trailing silence that ends a sentence. Raise it in a noisy room. |
| `streaming.min_speech_ms` | How much detected speech a window needs before it is decoded. This is what stands between a breath and a sentence Whisper invented. |
| `streaming.max_window_seconds` | Caps how much audio one pass covers, so cost stays flat during a long monologue. |
| `server.port` | The port that language serves on. The two must differ; see above. |
| `server.host` | `0.0.0.0` (the shipped value) serves every interface, `127.0.0.1` only this machine. See [Security](#security). |
| `limits.max_sessions` | A hard gate. Over it, the server returns `server_busy` immediately rather than queueing behind a decoder that cannot catch up. |
| `logging.store_audio` / `store_transcript` | Both `false`, and they should stay that way. See below. |

### Every setting

The file has six sections and nothing else. An unknown section, or an unknown
key inside one, is a startup error rather than a shrug — which matters most for
`logging.store_audio`, where a misspelling would look like it had been set.

Defaults below are the code's own fallbacks. The shipped configs set most of
them explicitly, and the installer rewrites every path in them to point inside
the prefix it installed to.

#### `server`

| Key | Default | Accepts | What it does |
| --- | --- | --- | --- |
| `host` | `0.0.0.0` | any local address | `0.0.0.0` serves every interface, `127.0.0.1` only this machine. See [Security](#security) before opening it. |
| `port` | `8765` | 1–65535 | The two languages must differ; the shipped English config uses `8766`. |
| `instance_name` | `local-dictation` | any string | Reported to clients in the `ready` event; the shipped configs name the language. Worth setting when several hosts serve one language behind a single address. |

#### `model`

Handed to the inference backend. `device`, `compute_type`, `cpu_threads` and
`num_workers` are CTranslate2 settings that the `mlx` backend ignores — see
[Choosing a backend](#choosing-a-backend).

| Key | Default | Accepts | What it does |
| --- | --- | --- | --- |
| `path` | `<prefix>/models/large-v3-turbo` | a directory holding `model.bin` | A CTranslate2 conversion, never a HuggingFace repo id: the server makes no outbound requests. `check` opens it. |
| `device` | `cpu` | `whisper`: `cpu`, `cuda`, `auto`. `openvino`: `GPU`, `GPU.1`, `CPU`, `NPU` | Read by the `whisper` and `openvino` backends; `mlx` ignores it. `cuda` needs a CTranslate2 built with CUDA and an NVIDIA GPU; **on Apple Silicon no value here reaches the GPU**, because CTranslate2 has no Metal backend — that is what [`--backend mlx`](#choosing-a-backend) is for. Under `openvino` this is an OpenVINO device string and the ones that defer the choice (`AUTO`, `HETERO`, `MULTI`) are refused. |
| `compute_type` | `int8` | on CPU: `int8`, `int8_float32`, `float32` | Ask your own build rather than guessing: `python -c "import ctranslate2; print(ctranslate2.get_supported_compute_types('cpu'))"`. `int8` is what the latency budget assumes. |
| `language` | `ko` | `ko`, `en` | There is no auto-detection. Each server transcribes one language, which is what makes a swapped port a detectable mistake rather than fluent nonsense. |
| `beam_size` | `1` | ≥ 1 | `1` is greedy. Beam search roughly doubles wall-clock for an accuracy gain dictation — where the text is appearing as you watch — does not benefit from. |
| `temperature` | `0.0` | a float | `0.0` is deterministic. Above it the decoder samples, which on hard audio means invented words rather than a worse guess. |
| `cpu_threads` | `0` | `0` = let CTranslate2 choose | Pin it to the physical core count after running the benchmark below; hyperthreads make INT8 slower. Also sets `OMP_NUM_THREADS`. |
| `num_workers` | `1` | ≥ 1 | Raising it buys nothing here: CTranslate2 models are not safe to call concurrently, so the server serialises decodes behind a lock and `limits.max_sessions` keeps the queue short. |
| `draft_path` | `null` | a directory, or `null` | A small model (`base`) used **only** for live partial text. The single biggest latency change available: first partial goes from about 3.7 s to about 0.9 s. Nothing it produces is committed — the accurate model decodes the utterance once at the end, and that is the text you keep. Download it with `fetch-model.sh base`; see [model-setup.md](model-setup.md) and [latency.md](latency.md). |
| `initial_prompt` | `null` | a short string, or `null` | Vocabulary hint prepended to every decode: names, product terms, jargon. Keep it under ~200 characters — it is charged against the context window on every pass. |
| `condition_on_previous_text` | `false` | `true` / `false` | `true` feeds already-decoded text back as context. Off by default because this server prefers latency: it costs an extra prompt on every pass, and it lets one bad decode influence everything after it. |

#### `streaming`

| Key | Default | Accepts | What it does |
| --- | --- | --- | --- |
| `chunk_ms` | `600` | ≥ 100 | How much new audio to gather before the next decode pass. Lower is snappier and costs proportionally more CPU. |
| `silence_ms` | `600` | ≥ 100 | Trailing silence that ends an utterance. Raise it in a noisy room, or if you pause mid-sentence to think. |
| `max_utterance_seconds` | `120` | ≥ 5 | Hard cap. The utterance is force-finalized and the client gets a non-fatal error. |
| `max_window_seconds` | `12.0` | ≥ 3, and ≤ `max_utterance_seconds` | The longest stretch one decode pass may cover. Past it, audio whose text is already committed is dropped and carried forward as a prompt — this is what keeps cost per pass flat however long someone talks. |
| `agreement_window` | `2` | ≥ 2 | How many consecutive hypotheses must agree before a prefix is committed. `2` is LocalAgreement-2; `3` commits less and shows text later. |
| `min_speech_ms` | `120` | 0–1000 | How much *detected speech* a window must hold before it is sent to the decoder. Handed silence, Whisper does not answer with silence — it answers with the boilerplate its training subtitles ended on, confidently, and nothing downstream can tell that apart from a real sentence. Raise it if a phantom line still slips through; every 10 ms you add is 10 ms of a real short word you risk dropping, and the shortest measured one is 290 ms. `0` decodes anything the detector twitched at, which is what produced the phantoms. |
| `vad` | `silero` | `silero`, `energy`, `none` | `silero` is a speech model and the only one that holds up in a noisy room. `energy` is a plain RMS threshold. `none` treats everything as speech, so utterances end only when you stop or the cap fires. |
| `energy_threshold` | `0.006` | RMS, `0.0`–`1.0` | Only read by the energy detector. |
| `silero_model_path` | `<prefix>/models/silero_vad.onnx` | a path, or `null` | Required when `vad` is `silero`. If the file is missing at startup the server logs a warning and falls back to the energy detector rather than refusing to serve — `check` reports this as a warning, not a failure. |

#### `security`

All four are `null`/`false` in the shipped configs. [Security](#security) has the
reasoning and the alternatives.

| Key | Default | Accepts | What it does |
| --- | --- | --- | --- |
| `tls_certificate` | `null` | a PEM path | Serves `wss://` instead of `ws://`. Must be set together with the key. |
| `tls_private_key` | `null` | a PEM path | |
| `client_ca` | `null` | a CA bundle path | Verifies client certificates against this. |
| `require_client_certificate` | `false` | `true` / `false` | Refuses a client that presents no certificate signed by `client_ca`. **This is the only access control the server has** — there is no token and no password. |

#### `limits`

| Key | Default | Accepts | What it does |
| --- | --- | --- | --- |
| `max_sessions` | `2` | ≥ 1 | Concurrent sessions. Over it the server answers `server_busy` immediately rather than queueing behind a decoder that cannot catch up. Counted per language server — see [Capacity](#capacity). |
| `max_audio_frame_bytes` | `65536` | ≥ 640 | Largest audio frame a client may send; a bigger one is answered with `audio_format_invalid`. 640 bytes is one 20 ms frame at 16 kHz mono, and the WebSocket's own frame ceiling is set to twice this. |
| `idle_timeout_seconds` | `60` | seconds | Closes a session that has sent no audio for this long, with `session_timeout`. |
| `handshake_timeout_seconds` | `10` | seconds | Refuses a connection whose `start` message takes longer than this to arrive. |

#### `logging`

| Key | Default | Accepts | What it does |
| --- | --- | --- | --- |
| `level` | `INFO` | `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL` | `DEBUG` is loud and worth it while diagnosing segmentation. |
| `json` | `true` | `true` / `false` | One JSON object per line, or human-readable lines. `false` is easier to read in a terminal. |
| `store_audio` | `false` | `true` / `false` | See [Retention](#retention). |
| `store_transcript` | `false` | `true` / `false` | See [Retention](#retention). |

### What the server refuses to start with

`check` runs the same validation the server does, so these are messages before a
restart rather than a process that exits four seconds after `start` said it was
up:

- `model.language` that is not `ko` or `en`; `server.port` outside 1–65535
- `chunk_ms` or `silence_ms` below 100 — the first wastes CPU on redundant
  decodes, the second chops utterances mid-word
- `agreement_window` below 2, `max_utterance_seconds` below 5,
  `max_window_seconds` below 3 or above `max_utterance_seconds`
- `min_speech_ms` outside 0–1000 — at the top of that range it is already
  discarding words people said
- `max_sessions` below 1, `max_audio_frame_bytes` below one 20 ms frame
- `vad: silero` with no `silero_model_path`
- **A half-configured TLS pair.** Certificate without key, or the reverse;
  `require_client_certificate` without a `client_ca`; either of those without a
  certificate. All-or-nothing on purpose — a listener that looks encrypted and
  is not is worse than a plain one.

Beyond those, `check` also opens every file the config names, which
`--check-config` deliberately does not — see [Everyday commands](#everyday-commands).

## Capacity

Whisper on CPU does not degrade gracefully. A third concurrent session does not
make everyone 50% slower — it makes everyone miss the latency budget while a
queue grows behind a decoder that was already at its limit. So capacity is
refused, not queued: over `limits.max_sessions` the server answers `server_busy`
and the client shows a recoverable error.

Size it from the measured real-time factor. Keep `rtf_p95` below 1.0 for a
single session before allowing a second.

## Metrics

Prometheus text format on `/metrics`, and a compact JSON snapshot on `/status`:

```bash
curl -s http://127.0.0.1:8765/status | python3 -m json.tool
```

| Series | What it tells you |
| --- | --- |
| `dictation_first_partial_seconds` | How quickly text appears. Target P95 ≤ 2 s. |
| `dictation_finalization_seconds` | How long a stop takes. Target P95 ≤ 1.5 s. |
| `dictation_real_time_factor` | Decode wall-clock over audio duration. Must stay below 1.0. |
| `dictation_sessions_rejected_total` | Rising means you are out of capacity. |
| `dictation_errors_total` | By code, so a spike is immediately attributable. |

A `null` percentile in `/status` means "above the largest bucket", which is its
own answer.

None of these carry content. That is the point of them being counts and timings.

## Retention

`store_audio` and `store_transcript` are `false` by default and the code
enforces it rather than trusting call sites: a logging filter redacts any record
carrying transcript text, and the test suite fails if a full session leaves
recognisable text in the logs. Audio buffers are dropped when a session ends.

Turning either on is a decision someone should be able to find afterwards, so
the server logs a warning at startup when it happens.

## Security

**Every install ships open and unencrypted:** `0.0.0.0`, TLS off, no client
certificates. Anything that can reach port 8765 or 8766 can use the server, and
nothing on those ports is encrypted. That is deliberate — the deployment this
exists for is one machine holding the model and laptops dictating into it, and
the loopback default it replaced meant every such install began with the same
hand edit to two files — but it belongs on a network you trust and nowhere else.

What crosses the wire is your voice and the text it became, which is usually the
most sensitive writing anyone does. Two ways to protect it:

- **An SSH tunnel**, which needs nothing from this server at all. Install with
  `--loopback` (or set `server.host` back to `127.0.0.1`), then forward the
  ports from the client machine:

  ```bash
  ssh -N -L 8765:127.0.0.1:8765 -L 8766:127.0.0.1:8766 you@server
  ```

  The client connects to `127.0.0.1` with TLS off. SSH is already doing both
  jobs, so no certificate is involved. The cost is a session that has to be up
  while you dictate.

- **TLS** with your own certificate authority, and `require_client_certificate:
  true`. Worth being plain about why: mutual TLS is the *only* access control
  this server has. There is no token and no password, so with TLS off there is
  no authentication of any kind. Both settings are all-or-nothing — a
  half-configured listener is worse than a plain one, because operators assume
  it is encrypted — so the server refuses to start on a partial TLS config.
  `check` reads the certificate files, so run it before the restart.

Two more that hold either way:

- **No egress.** The server never fetches anything. Block outbound traffic and
  nothing breaks.
- **Two inbound ports** and the health paths. Nothing else needs to be open.

## Upgrading

```bash
local-dictation-server update
```

With no argument it asks the release page for the newest server tarball,
downloads it, checks it against the release's published `SHA256SUMS`, and
upgrades. If the installed version is already the latest it says so and stops
there — nothing is restarted, so running this on a schedule costs a request and
no downtime.

```
installed 0.1.4, latest 0.1.5
downloading local-dictation-server-0.1.5.tar.gz
checksum ok
stopping ko en for the update
```

Pass a path instead when the host has no egress, or when you want a specific
version:

```bash
local-dictation-server update ~/downloads/local-dictation-server-0.1.5.tar.gz
```

`update --force` reinstalls the version you already have. `LD_RELEASE_REPO`
points the lookup at a fork or a mirror.

Either way: stop, install over the same prefix, check, start again exactly what
was running.
Doing it in that order is the point of the command: installing under a live
server leaves a process running code that is no longer on disk, and starting
before checking starts something that cannot serve. If either the install or the
check fails, nothing is started and the reason is printed.

The installer never overwrites a config you have edited; it reports that it kept
yours. Models are untouched. A language you had deliberately stopped stays
stopped.

Then confirm the running processes caught up:

```bash
local-dictation-server version
```

A version older than 0.1.5 cannot fetch its own successor — it does not have the
code that does it. That first hop is one manual step, in
[server-install.md](server-install.md#the-first-hop), along with the rest of the
detail.

## When something goes wrong

**A server will not start.** `start` prints the tail of the log that explains
it, and `local-dictation-server logs ko` has the rest. The usual causes are a
model path that does not exist, a TLS file that is not readable, and a port
already in use.

**`start` says a directory is not writable.** A `sudo ./install.sh` from before
this was fixed left `run/` and `log/` owned by root while documenting every
command without sudo. Take them back:

```bash
sudo chown -R "$(id -un)" /opt/local-dictation/{run,log,models}
```

**`health` says `not_ready` for a long time.** The model is loading. `large-v3`
from a cold page cache takes a while; `/status` flips to `ready` when the warmup
decode finishes.

**Everything works but transcription is empty.** Check the log for a line about
the VAD falling back to the energy detector. The server cross-checks its voice
detector against raw signal level, so a model file that is missing or the wrong
export reports itself instead of quietly treating every session as silence.

**Sentences appear that nobody said** — "감사합니다", "다음 영상에서 만나요", a
bare "!". Whisper's answer to a window with no speech in it is not silence: it
is the boilerplate its training subtitles ended on, delivered with the model's
own no-speech probability at 0.00 and an average log-probability in the same
range as real speech. Nothing after the decoder can tell the two apart, so the
audio must not reach it. `streaming.min_speech_ms` is the gate; raise it in a
room noisy enough that the detector keeps opening utterances on nothing, and
raise it slowly — the shortest real word measures about 290 ms of detected
speech, and the default sits at 120 ms.

None of the three inference backends helps here. faster-whisper's `vad_filter`
runs the same Silero model at the same threshold as the session does, so
anything that got past one gets past the other; MLX and OpenVINO have no
equivalent at all.

The gate is also only as good as the detector behind it. `vad: energy` is an
RMS threshold and cannot tell a breath from a word — a breath clears 0.006 RMS
comfortably — so on a host that fell back to it, expect to raise
`min_speech_ms` well above the default, or better, put `silero_vad.onnx` where
`silero_model_path` points.

**A phrase is typed twice.** Once a sentence outgrows `max_window_seconds` the
committed text goes back to the decoder as a prompt so the trimmed window still
knows what it is in the middle of — and Whisper is free to carry that prompt
into its output. Measured on the same clip, prompt the only difference:

```
audio from 0.18s  prompt=''      -> '회의에서는 지난 분기 실적과 …'
audio from 0.18s  prompt='오늘 '  -> '오늘 회의에서는 지난 분기 실적과 …'
```

"오늘" is not in that audio — it was trimmed away because it had already been
committed and typed. The server cuts the repeat off at the join; if you are
seeing this, the server is older than the client.

**Latency is bad.** [latency.md](latency.md). Start with `model.draft_path`.

## Measuring accuracy and latency

`benchmark.py` streams recordings through the real protocol at real time and
scores them, so the numbers describe what a user experiences rather than what a
batch decode of a whole file would suggest.

Write a manifest, one JSON object per line:

```json
{"audio": "clips/ko-001.wav", "reference": "오늘 오후 세 시에 회의를 시작합니다.", "language": "ko"}
```

Clips must be 16 kHz mono 16-bit WAV — the format the client sends.

```bash
/opt/local-dictation/venv/bin/python \
  /opt/local-dictation/app/scripts/benchmark.py \
  --manifest cases.jsonl --port 8765 --out report.txt
```

```
Accuracy
  ko: CER 3.2%  WER 29.5%  (2 clip(s))

Latency
  first partial  p50 0.93s  p95 0.93s   target p95 <= 2.0s — meets
  finalization   p50 4.59s  p95 4.59s   target p95 <= 1.5s — MISSES
```

**Read CER for Korean and WER for English.** Korean is written without
consistent word spacing and Whisper renders numbers as digits, so one "세 시"
coming back as "3시" moves WER enormously on a short clip while CER barely
notices — the 29.5% above is almost entirely that. English words are the unit a
reader perceives, so WER is the honest number there.

Nothing is stored: the report contains your own reference text and the
hypotheses for the clips you supplied, and nothing reaches the server's logs.

## Publishing an update

Clients only install what your key signed. Generate the key once, keep the
private half wherever your release secrets live, and put the public half in each
client's settings:

```bash
cd client && go run ./cmd/sign-manifest keygen -out release-key
```

Then, per release:

```bash
go run ./cmd/sign-manifest hash dist/LocalDictation-0.2.0.pkg dist/LocalDictation-0.2.0-x64.msi
$EDITOR manifest.json     # start from client/cmd/sign-manifest/manifest.example.json
go run ./cmd/sign-manifest sign -key release-key.private -manifest manifest.json
```

Signing refuses a manifest that is not publishable — a plain-HTTP URL, a missing
hash, a zero size — and re-verifies the file it just wrote, so a serialisation
difference between signing and publishing cannot reach a user's machine. Serve
`manifest.json` and the artefacts from the internal HTTPS URL in
`update.manifest_url`.

A manifest whose signature does not verify is refused outright by the client,
not reported as a warning.
