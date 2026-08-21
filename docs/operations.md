# Operating the servers

For whoever runs the shared servers. If you use standalone mode, you do not need
this file — the client manages its own server.

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

`check` validates both config files without binding a port; run it before a
restart.

## Configuration

One YAML file per language, in `$LD_HOME/config`. Every setting can also be
overridden by an environment variable, which is how a host-specific path gets
injected without editing a shipped file:

```bash
LOCAL_DICTATION_MODEL__PATH=/mnt/models/large-v3-turbo local-dictation-server restart all
```

The pattern is `LOCAL_DICTATION_<SECTION>__<KEY>`, upper-cased. An unknown
section or key is an error at startup rather than a silently ignored typo —
which matters most for `logging.store_audio`, where a misspelling would look
like it had been set.

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

The shipped configs assume a closed network and start from the strict end:

- **TLS** with your internal CA, and `require_client_certificate: true` so only
  managed machines can connect. Both are all-or-nothing — a half-configured
  listener is worse than a plain one, because operators assume it is encrypted,
  so the server refuses to start on a partial TLS config.
- **No egress.** The server never fetches anything. Block outbound traffic and
  nothing breaks.
- **Two inbound ports** and the health paths. Nothing else needs to be open.

For a first bring-up on a trusted network you can set the TLS fields to `null`
and `require_client_certificate` to `false`. Turn them back on before anyone
relies on it.

## Upgrading

```bash
local-dictation-server stop all
tar xzf local-dictation-server-<version>.tar.gz
cd local-dictation-server-<version> && sudo ./install.sh
local-dictation-server check all && local-dictation-server start all
local-dictation-server health all
```

The installer never overwrites a config you have edited; it reports that it kept
yours. Models are untouched.

## When something goes wrong

**A server will not start.** `local-dictation-server logs ko` has the reason.
The usual causes are a model path that does not exist and a TLS file that is not
readable by the service account.

**`health` says `not_ready` for a long time.** The model is loading. `large-v3`
from a cold page cache takes a while; `/status` flips to `ready` when the warmup
decode finishes.

**Everything works but transcription is empty.** Check the log for a line about
the VAD falling back to the energy detector. The server cross-checks its voice
detector against raw signal level, so a model file that is missing or the wrong
export reports itself instead of quietly treating every session as silence.

**Latency is bad.** [latency.md](latency.md). Start with `model.draft_path`.
