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
local-dictation-server logs ko --follow
local-dictation-server restart en
local-dictation-server stop all
```

`status` asks the operating system whether a process is alive. `health` asks the
server whether it can actually transcribe — a process can be running while the
model is still loading, and that difference is usually what you are checking.

`check` validates both config files and opens every file they name — the model,
the VAD model, the certificates — without binding a port. Run it before a
restart: a model directory that is not there is a message here rather than a
server that exits four seconds after `start` reported it as up.

`start` waits up to `LD_START_TIMEOUT` seconds (default 15) for the new process
to either fail or bind its port, so a start that cannot work is reported as a
failure with the reason from the log. Loading a model takes minutes and happens
before the bind, so a real start usually reaches that timeout and says it is
still loading; `health` is what answers when it can transcribe.

## Where things live

The management command finds everything through `LD_*`. A wrapper written by the
installer sets them to the prefix you installed into, so you only set them by
hand when running from a checkout — or to point one command somewhere else,
which they still allow.

| Variable | Default | What it names |
| --- | --- | --- |
| `LD_HOME` | `/opt/local-dictation` | The install root the rest default to |
| `LD_APP_DIR` | `$LD_HOME/app` | The directory *containing* `app/` |
| `LD_CONFIG_DIR` | `$LD_HOME/config` | `server-ko.yaml` and `server-en.yaml` |
| `LD_PYTHON` | `$LD_HOME/venv/bin/python` | The interpreter |
| `LD_RUN_DIR` | `$LD_HOME/run` | PID files; must be writable by you |
| `LD_LOG_DIR` | `$LD_HOME/log` | Log files; must be writable by you |
| `LD_BACKEND` | `whisper` | `fake` starts without a model, for plumbing checks |
| `LD_START_TIMEOUT` | `15` | Seconds `start` watches for an early exit; `0` not to wait |

## Configuration

One YAML file per language, in `$LD_CONFIG_DIR`. Every setting can also be
overridden by an environment variable, which is how a host-specific path gets
injected without editing a shipped file:

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

### Settings worth understanding

| Setting | Why it matters |
| --- | --- |
| `model.path` | A local directory. The server never reaches the internet; a wrong path fails at startup instead of hanging on first use. |
| `model.draft_path` | Optional small model for live partial text. See [latency.md](latency.md); this is the single biggest latency change available. |
| `model.cpu_threads` | `0` lets CTranslate2 choose. Pin it to the physical core count after benchmarking; hyperthreads make INT8 slower. |
| `streaming.chunk_ms` | How often a decode pass runs. Lower is snappier and costs more CPU. |
| `streaming.silence_ms` | Trailing silence that ends a sentence. Raise it in a noisy room. |
| `streaming.max_window_seconds` | Caps how much audio one pass covers, so cost stays flat during a long monologue. |
| `limits.max_sessions` | A hard gate. Over it, the server returns `server_busy` immediately rather than queueing behind a decoder that cannot catch up. |
| `logging.store_audio` / `store_transcript` | Both `false`, and they should stay that way. See below. |

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

The installer writes the posture that matches where it installed: a prefix you
own gets `127.0.0.1` with TLS off, a `sudo` install into `/opt` gets `0.0.0.0`
with TLS on and client certificates required. Neither is a default you should
leave unexamined once other machines depend on it.

For a server people connect to:

- **TLS** with your internal CA, and `require_client_certificate: true` so only
  managed machines can connect. Both are all-or-nothing — a half-configured
  listener is worse than a plain one, because operators assume it is encrypted,
  so the server refuses to start on a partial TLS config. `check` reads the
  certificate files, so run it before the restart.
- **No egress.** The server never fetches anything. Block outbound traffic and
  nothing breaks.
- **Two inbound ports** and the health paths. Nothing else needs to be open.

The pairing that should never happen by accident is `0.0.0.0` with TLS off. It
takes a deliberate edit from either starting point, which is the point.

## Upgrading

```bash
local-dictation-server stop all
tar xzf local-dictation-server-<version>.tar.gz
cd local-dictation-server-<version> && ./install.sh --prefix <your prefix>
local-dictation-server check all && local-dictation-server start all
local-dictation-server health all
```

The installer never overwrites a config you have edited; it reports that it kept
yours. Models are untouched.

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
