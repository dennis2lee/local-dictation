# Local Dictation protocol v1

A dictation session is a single WebSocket connection to one language server.
Control messages are JSON text frames; audio is raw binary frames. There is no
multiplexing: one connection carries exactly one dictation session.

```
client                                  server
  |  WSS connect /v1/dictation            |
  | ------------------------------------> |
  |  {"type":"start", ...}      (text)    |
  | ------------------------------------> |
  |            {"type":"ready", ...}      |
  | <------------------------------------ |
  |  PCM frame                  (binary)  |
  | ------------------------------------> |
  |       {"type":"transcript", ...}      |
  | <------------------------------------ |
  |            ... repeats ...            |
  |  {"type":"flush"}           (text)    |
  | ------------------------------------> |
  |  {"type":"transcript","final":true}   |
  | <------------------------------------ |
  |  {"type":"stop"}            (text)    |
  | ------------------------------------> |
  |            {"type":"closed", ...}     |
  | <------------------------------------ |
  |               close 1000              |
```

## Endpoint

    wss://<host>:<port>/v1/dictation

The port selects the language. There is no automatic language detection: the
Korean server only ever emits Korean, the English server only ever emits English.
The `language` field in `start` is an assertion the server verifies, not a
request it honours.

| Language | Default port | Server `language` |
| -------- | ------------ | ----------------- |
| Korean   | 8765         | `ko`              |
| English  | 8766         | `en`              |

## Audio format

Binary frames are raw PCM with no container and no header:

| Property     | Value                       |
| ------------ | --------------------------- |
| Encoding     | `pcm_s16le`                 |
| Sample rate  | 16000 Hz                    |
| Channels     | 1 (mono)                    |
| Frame size   | 20–40 ms (640–1280 bytes)   |

A binary frame with an odd length is a protocol violation and yields
`error.code = "audio_format_invalid"`. Frames may be any size the client
chooses within the server's `max_audio_frame_bytes` limit; the 20–40 ms
guidance keeps latency low without paying per-frame overhead.

## Client to server

### `start`

Must be the first message. Sending audio before `start` is an
`audio_before_start` error.

```json
{
  "type": "start",
  "protocol_version": 1,
  "session_id": "s-01J6ZH8Q4T7N2WQ4C4Q0M2Y6ZB",
  "client_version": "0.1.0",
  "language": "ko",
  "audio": {
    "encoding": "pcm_s16le",
    "sample_rate": 16000,
    "channels": 1
  }
}
```

`session_id` is opaque to the server and is echoed into logs and metrics. It must
not contain user content — the client generates a random identifier.

### `flush`

Sent once the client has stopped capturing. The server decodes whatever audio
remains in the buffer and emits a `transcript` with `"final": true`. Audio frames
sent after `flush` are ignored.

```json
{"type": "flush"}
```

### `stop`

Ends the session. The server replies with `closed` and closes the socket with
code 1000. A client that simply disconnects is also valid — the server discards
the session either way — but `stop` gives the client a positive acknowledgement.

```json
{"type": "stop"}
```

## Server to client

### `ready`

```json
{
  "type": "ready",
  "protocol_version": 1,
  "session_id": "s-01J6ZH8Q4T7N2WQ4C4Q0M2Y6ZB",
  "language": "ko",
  "model": "large-v3",
  "server_version": "0.1.0"
}
```

### `transcript`

```json
{
  "type": "transcript",
  "protocol_version": 1,
  "utterance_id": "u-01J6ZH8Q4T7N2WQ4C4Q0M2Y6ZB-0003",
  "revision": 12,
  "stable": "오늘 오후 세 시에",
  "partial": "회의를 시작합니다",
  "final": false
}
```

| Field          | Meaning                                                          |
| -------------- | ---------------------------------------------------------------- |
| `utterance_id` | Identifies one utterance. Resets `stable` to `""` when it changes. |
| `revision`     | Strictly increasing per session. Stale revisions are discarded.   |
| `stable`       | Committed prefix of the current utterance.                        |
| `partial`      | Volatile tail. The client renders it as composing text.           |
| `final`        | `true` on the last transcript for this utterance.                 |

`stable + partial` reconstructs the hypothesis verbatim, so `stable` carries the
separator that precedes `partial` — usually a trailing space. Clients must not
insert a space of their own between the two.

### `error`

```json
{
  "type": "error",
  "protocol_version": 1,
  "code": "server_busy",
  "message": "concurrent session limit reached",
  "fatal": true
}
```

| Code                   | Fatal | Cause                                                   |
| ---------------------- | ----- | ------------------------------------------------------- |
| `protocol_unsupported` | yes   | `protocol_version` is not implemented by this server.    |
| `language_mismatch`    | yes   | `start.language` differs from the server's language.     |
| `server_busy`          | yes   | Concurrent session limit reached. Fail fast, never queue.|
| `audio_format_invalid` | yes   | Declared or actual audio format is not `pcm_s16le/16k/1`.|
| `audio_before_start`   | yes   | Binary frame arrived before `start`.                     |
| `utterance_too_long`   | no    | Utterance exceeded `max_utterance_seconds`; force-finalized. |
| `inference_failed`     | no    | One decode pass failed. The session continues.           |
| `session_timeout`      | yes   | No audio within the idle timeout.                        |
| `malformed_message`    | yes   | JSON was unparseable or failed schema validation.        |
| `internal_error`       | yes   | Unclassified server fault.                               |

A non-fatal error leaves the session open; the client keeps `stable`, drops
`partial`, and carries on. A fatal error is always followed by socket closure.

### `closed`

```json
{"type": "closed", "protocol_version": 1, "reason": "client_stop"}
```

Reasons: `client_stop`, `server_shutdown`, `idle_timeout`, `error`.

## Invariants

These hold for every conforming server, and clients may rely on them:

1. **`stable` is append-only within an utterance.** For two transcripts of the
   same `utterance_id` with `revision` a < b, `stable_a` is a prefix of
   `stable_b`. A server that would have to retract committed text must instead
   start a new `utterance_id`.
2. **`revision` strictly increases within a session,** across utterance
   boundaries. Clients discard any transcript whose `revision` is not greater
   than the last one applied — this is what makes out-of-order delivery and
   reconnection safe.
3. **`final: true` is terminal for its utterance.** No further transcript
   carries the same `utterance_id`.
4. **`partial` is never committed by the server.** Only text that has appeared
   in `stable` (or in the `stable` of a `final` transcript) is committed output.
5. **`stable + partial` is the client's best current rendering** of the utterance.

## Version negotiation

The client sends the highest version it implements. A server that does not
implement it responds with `protocol_unsupported` and lists what it does support
in the message. The client does not downgrade automatically — an operator sees an
explicit error rather than a silently degraded session.
