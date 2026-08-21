# Local Dictation

Offline Korean and English dictation that types at your cursor. Press a
shortcut, speak, press it again — the text appears in whatever application you
were already using. Audio never leaves the machine that transcribes it.

```
  Ctrl+Shift+M ──▶  🎤  ──▶  Whisper large-v3 (CPU, INT8)  ──▶  your cursor
                            on this machine, or on your own server
```

- **Private.** Nothing is uploaded, nothing is written to disk, nothing is
  logged. Enforced in code and asserted by tests, not left to convention.
- **Runs standalone.** The client starts its own speech server on your machine.
  No infrastructure, no network.
- **Or shared.** Point it at servers someone else runs. Same protocol, same
  behaviour, one setting apart.
- **Two languages, two servers.** No automatic detection: a Korean server
  transcribes Korean and an English one transcribes English, so the output is
  predictable and each is separately observable.
- **Windows and macOS**, with an Ubuntu or macOS server for shared deployments.

## Getting started

1. Install the client — [installation.md](docs/installation.md)
2. Install a speech model, one command — [model-setup.md](docs/model-setup.md)
3. Dictate — [usage.md](docs/usage.md)

```bash
# macOS, after installing the app
"/Applications/Local Dictation.app/Contents/Resources/server/scripts/fetch-model.sh" \
  large-v3-turbo --dest ~/Library/Application\ Support/LocalDictation/models

"/Applications/Local Dictation.app/Contents/MacOS/local-dictation" --check
```

Models are never bundled: they are 1.5–2.9 GB, they carry their own licence, and
every site mirrors them differently.

## How it fits together

```
Windows / macOS                        this machine, or a server
┌───────────────────────────┐          ┌──────────────────────────────┐
│ Go client                 │          │ Python server, one language  │
│  UI · hotkey · microphone │  16 kHz  │  FastAPI · WebSocket         │
│  cursor composition       │  mono ── │  faster-whisper large-v3     │
│                           │   PCM    │  Silero VAD · LocalAgreement │
│                           │ ◀─────── │  CPU INT8                    │
└───────────────────────────┘ partial  └──────────────────────────────┘
                              stable
                              final
```

The client captures audio and owns the cursor. The server owns inference. They
meet at a versioned WebSocket protocol ([protocol/v1](protocol/v1/protocol.md))
whose invariants are what make the text at your cursor trustworthy: committed
text only ever grows, revisions only ever increase, and a dropped connection
loses the guess but never the sentence.

Standalone mode is the same picture with the server on loopback, started and
supervised by the client. There is one inference implementation and one
streaming policy either way.

## Repository layout

| Path | What is in it |
| --- | --- |
| [`protocol/`](protocol) | The wire contract: spec and JSON Schemas, versioned |
| [`server/`](server) | Python server — API, streaming, inference, config, scripts |
| [`client/`](client) | Go client — UI, session, audio, transport, text input |
| [`build/`](build) | Release builds: macOS pkg/dmg, Windows MSI, server tarball |
| [`docs/`](docs) | Installation, usage, model setup, latency, operations |
| [`doc/`](doc) | The original project plan |

## The latency problem, and the fix

Whisper's encoder always processes a 30-second padded window, so one decode pass
costs the same whether it covers three seconds or ten — about 3.2 s for
`large-v3-turbo` on eight CPU cores. That fixed cost is the floor on how quickly
partial text can appear, and it is well above the 2 s target.

Setting `model.draft_path` to a small model changes the picture completely.
Partials come from the draft; the accurate model decodes each utterance once, at
the end, and only its reading is ever committed.

| | one model | draft `base` + `large-v3-turbo` |
| --- | --- | --- |
| First partial, English | 3.69 s | **0.88 s** |
| First partial, Korean | 3.76 s | **0.89 s** |
| Final text | *identical* | *identical* |

Measured end to end through the real protocol. Details, including what does
*not* help, are in [latency.md](docs/latency.md).

## Building from source

```bash
# Server
cd server && python3 -m venv .venv && .venv/bin/pip install -e '.[dev,inference]'
.venv/bin/python -m pytest

# Client
cd client && go test ./... && go build ./cmd/local-dictation

# Everything, for release
build/release.sh --version 0.1.0
```

`build/release.sh` runs both test suites first, then produces the server
tarball, the macOS `.pkg` and `.dmg`, and the Windows payload — with a
`SHA256SUMS` beside them. The Windows MSI needs a Windows machine with the WiX
toolset; see [`build/windows/build-msi.ps1`](build/windows/build-msi.ps1).

## Testing

The tests cover the parts where being wrong is expensive:

- **Protocol** — every event validated against the JSON Schemas that the Go and
  Python bindings are both written from, so the two cannot drift.
- **Streaming** — the invariants that protect your document: committed text
  never retracts, revisions strictly increase, a final is terminal.
- **Cursor composition** — stale events discarded, partial text replaced without
  duplicating, backspaces counted in runes so Korean syllables survive.
- **Focus** — a session that outlives the window it started in stops instead of
  typing the rest of the sentence somewhere else.
- **Privacy** — a full session is run and the logs are searched for the text it
  produced.
- **End to end** — from an empty directory, the client builds a Python
  environment, starts a server, streams a real recording at real time, and the
  transcript is checked against what was said.

```bash
LOCAL_DICTATION_TEST_MODEL=/path/to/large-v3-turbo \
LOCAL_DICTATION_TEST_AUDIO=/path/to/speech.wav \
go test ./internal/e2e/ -run Standalone -v
```

## Licence

MIT — see [LICENSE](LICENSE). Speech models are downloaded separately and carry
their own terms.
