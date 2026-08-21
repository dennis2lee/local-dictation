# Installing a speech server

For a server other machines connect to, or one you run beside the client on your
own machine and manage yourself.

**You probably do not need this.** In standalone mode the client starts, supervises
and stops its own server. Install one by hand only when several machines share it,
or when you want to run and inspect the server yourself — see
[client-install.md](client-install.md) for the client.

Running a speech server needs no privileges. The default install goes into a
directory you own, and every command afterwards is yours to run.

## Before you start

| | |
| --- | --- |
| Python | 3.11 or newer, with `venv` |
| Disk | ~1 GB of dependencies, plus 1.5 GB for the model |
| CPU | Eight performance cores holds one session comfortably; see [server-usage.md](server-usage.md) for sizing |
| Network | Only to install. The server itself never reaches the internet |

On Ubuntu, `sudo apt install python3 python3-venv` covers the first row. On
macOS, the system Python 3 or a Homebrew one both work.

## 1. Install

```bash
tar xzf local-dictation-server-0.1.2.tar.gz
```

```bash
cd local-dictation-server-0.1.2 && ./install.sh
```

That is the whole install: no sudo, into `~/local-dictation`, with the
`local-dictation-server` command linked into `~/.local/bin`. If that directory is
not on your `PATH` the installer says so, and the full path it prints works
either way.

### Choosing where it goes

**`--prefix` puts the whole install anywhere you can write.** Everything the
server needs lives under that one directory — code, virtual environment, configs,
models, logs and pid files — so a prefix is the complete unit: back it up, move
it, delete it.

```bash
./install.sh --prefix ~/tools/local-dictation
```

```bash
./install.sh --prefix /srv/dictation
```

Nothing is written outside it except the `local-dictation-server` symlink, which
`--link DIR` also moves:

```bash
./install.sh --prefix ~/tools/local-dictation --link ~/bin
```

You can install several prefixes side by side — one per version, or one to try a
new model against — as long as their configs use different ports. Every command
then finds its own install through the wrapper at
`<prefix>/bin-local-dictation-server`.

Without `--prefix`, the default depends only on whether you used `sudo`:

| | `./install.sh` | `sudo ./install.sh` | `--prefix DIR` |
| --- | --- | --- | --- |
| Prefix | `~/local-dictation` | `/opt/local-dictation` | `DIR` |
| Command linked into | `~/.local/bin` | `/usr/local/bin` | the same, or `--link DIR` |
| Listens on | `127.0.0.1` | `0.0.0.0` | `127.0.0.1` unless run as root |
| TLS | off | on, certificates expected | off unless run as root |

The generated configs point **inside the prefix you chose**, so there are no
paths to edit before the first start. A root install hands `run/`, `log/` and
`models/` to the user who invoked `sudo`, so the everyday commands still need no
privileges.

The columns differ because they are different deployments. A prefix you own is a
machine talking to itself, so it binds loopback and skips TLS. A `/opt` install is
a shared server, so it binds every interface and expects certificates. The
combination nobody should reach by accident — open to the network with TLS off —
takes a deliberate edit either way.

The installer also builds a virtual environment under the prefix and installs the
server's Python dependencies into it, then **imports them to confirm they are
really there**. `pip` exiting successfully is not the same as the server being
able to start, and a partial install that is only discovered at the first command
shows up as a traceback rather than as something you can act on.

### What it creates

```
~/local-dictation/
├── app/            the Python package, its scripts and the protocol schemas
│   └── scripts/    fetch-model.sh, local-dictation-server, benchmark.py
├── config/         server-ko.yaml and server-en.yaml
├── models/         empty until step 2
├── venv/           the interpreter and dependencies
├── run/            pid files
├── log/            server-ko.log and server-en.log
└── tls/            certificates, if you use them
```

Re-running the installer over an existing prefix upgrades the code and leaves
`config/` and `models/` alone. It reports each config it kept — and
`local-dictation-server update` does that for you, in the order that cannot go
wrong. See [Updating](#updating) below.

## 2. Install a model

Models are never bundled: they are 1.5–2.9 GB, they carry their own licence, and
every site mirrors them differently.

```bash
~/local-dictation/app/scripts/fetch-model.sh large-v3-turbo --dest ~/local-dictation/models
```

This also fetches `silero_vad.onnx` (2.2 MiB), which decides when an utterance has
ended. The configs already name both, so nothing needs editing.

`large-v3-turbo` is the right starting point on CPU. `large-v3` is more accurate
and several times slower — [model-setup.md](model-setup.md) has the comparison,
offline transfer instructions and checksum verification.

## 3. Check, then start

```bash
local-dictation-server check all
```

`check` validates both configs **and opens every file they name** — the model, the
VAD model, the certificates — without binding a port. A path that is not there is
a message here rather than a server that exits seconds after being started:

```
FAILED: ko on 127.0.0.1:8765 would not serve:
  - model.path is not a directory: /home/you/local-dictation/models/large-v3-turbo
```

```bash
local-dictation-server start all
```

```bash
local-dictation-server health all
```

```
ko  {"status":"ready","language":"ko","model":"large-v3-turbo",...}
en  {"status":"ready","language":"en","model":"large-v3-turbo",...}
```

`start` watches each new process until it either exits or binds its port, so a
config it refuses is reported as a failure with the reason from the log. Loading a
model takes minutes and happens before the bind, so a real first start usually
reports that it is still loading — that is not an error. `health` is what answers
whether it can transcribe yet; `status` only asks the operating system whether a
process is alive.

## Two languages, two processes

| Service | Language | Default port |
| --- | --- | --- |
| `local-dictation-ko` | Korean | 8765 |
| `local-dictation-en` | English | 8766 |

Deliberately not one process serving both: restarting Korean must never interrupt
an English session. Every command takes `ko`, `en` or `all`.

There is no automatic language detection. A Korean server transcribes Korean and
an English one transcribes English, so the output is predictable and each is
separately observable.

## Serving other machines

An install into a prefix you own listens on loopback with TLS off. To let other
machines connect, edit both configs:

```yaml
server:
  host: "0.0.0.0"
security:
  tls_certificate: "<prefix>/tls/server.crt"
  tls_private_key: "<prefix>/tls/server.key"
  client_ca: "<prefix>/tls/internal-ca.crt"
  require_client_certificate: true
```

TLS is all-or-nothing: the server refuses to start on a half-configured pair,
because a listener that looks encrypted and is not is worse than a plain one.
Then `check` before you restart — it reads the certificate files, so an unreadable
one is caught before the port opens.

In the client's **Settings** tab, choose **Remote servers** and give it the host
and the two ports.

## Updating

```bash
local-dictation-server update local-dictation-server-0.1.4.tar.gz
```

That is the whole upgrade. It stops whatever is running, installs the new tree
over the same prefix, checks the result, and starts again **exactly what was
running** — a language you had deliberately stopped stays stopped. If the install
or the check fails, nothing is started and the reason is printed; your configs
and models are untouched either way.

Point it at an unpacked directory instead of a tarball if you already have one:

```bash
local-dictation-server update ~/downloads/local-dictation-server-0.1.4
```

Extra arguments go through to the installer, so a non-default link directory
survives an update:

```bash
local-dictation-server update local-dictation-server-0.1.4.tar.gz --link ~/bin
```

Afterwards, and any time you want to know whether the running processes match
what is installed:

```bash
local-dictation-server version
```

```
installed  0.1.4  (/home/you/local-dictation)
ko         0.1.4  running
en         0.1.3  running — restart it to pick up 0.1.4
```

An update needs the network, because it rebuilds the virtual environment. The
server itself still never reaches out; only the installer does.

**Coming from 0.1.2 or earlier**, which had no `update` command: run the new
tarball's installer against your existing prefix, then check and start.

```bash
local-dictation-server stop all
```

```bash
cd local-dictation-server-0.1.3 && ./install.sh --prefix ~/local-dictation
```

```bash
local-dictation-server check all && local-dictation-server start all
```

## Uninstalling

```bash
local-dictation-server stop all
```

```bash
rm -rf ~/local-dictation ~/.local/bin/local-dictation-server
```

For a `sudo` install, that is `/opt/local-dictation` and
`/usr/local/bin/local-dictation-server`, and removing them needs root.

## When the install does not work

**`cannot create ...: /opt is not writable`.** You asked for a prefix you do not
own. Run `./install.sh` with no arguments, or pass `--prefix` somewhere you can
write.

**`command not found: local-dictation-server`.** The link went into a directory
that is not on your `PATH`. Add `~/.local/bin` to it, or use the full path the
installer printed: `~/local-dictation/bin-local-dictation-server`.

**`no Python 3.11 or newer found`.** Install one and pass `--python
/path/to/python3`.

**`start` says a directory is not writable.** A `sudo ./install.sh` from before
version 0.1.2 left `run/` and `log/` owned by root while documenting every command
without sudo. Take them back:

```bash
sudo chown -R "$(id -un)" /opt/local-dictation/{run,log,models}
```

**`check` fails on the model.** The path in the config and the directory you
downloaded into disagree. `check` prints both halves of that.

**Everything starts but `health` says `not_ready` for a long time.** The model is
loading. `large-v3` from a cold page cache takes a while.

Day-to-day operation — metrics, capacity, retention, benchmarking, publishing
updates — is in [server-usage.md](server-usage.md).
