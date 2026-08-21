# Installation

Local Dictation has two halves, and how you install them depends on which of
two shapes you want:

| | What runs where | Install |
| --- | --- | --- |
| **Standalone** | The client starts its own speech server on your own machine. Nothing leaves it. | Client only |
| **Shared servers** | One or two machines run the speech servers; laptops connect to them. | Server, then client |

Standalone is the default and needs no server administration. Shared servers
make sense when the laptops are slow, or when several people should share one
well-provisioned box.

Both shapes need a **speech model**, which is never included in any package.
That is one command, and it is [step 3](#3-install-a-speech-model) below.

---

## 1. Install the client

### macOS

Download `LocalDictation-<version>.pkg` and open it, or:

```bash
sudo installer -pkg LocalDictation-0.1.1.pkg -target /
```

A `.dmg` is also published if you prefer to drag the app to Applications.

If the package is not signed with a Developer ID, macOS will refuse the first
launch. Right-click the app and choose **Open**, or:

```bash
xattr -dr com.apple.quarantine "/Applications/Local Dictation.app"
```

### Windows

Run `LocalDictation-<version>-x64.msi`. It installs to
`C:\Program Files\Local Dictation` and adds Start menu and desktop shortcuts.

For an unattended install:

```powershell
msiexec /i LocalDictation-0.1.1-x64.msi /qn /l*v install.log
```

### What the installer does not include

- **No speech model.** 1.5–2.9 GB, its own licence, and every site mirrors it
  differently. See step 3.
- **No Python runtime.** The client finds an interpreter and builds its own
  isolated environment the first time you dictate. Bundling one would triple
  the download and pin a version your site may not want.

---

## 2. Install Python (standalone mode only)

The speech server is Python. Standalone mode needs **Python 3.11 or newer** on
the same machine.

```bash
python3 --version
```

If that is missing or too old:

- **macOS** — `brew install python` or download from
  [python.org](https://www.python.org/downloads/).
- **Windows** — `winget install Python.Python.3.12`, or the python.org
  installer. Tick **Add python.exe to PATH**.
- **Ubuntu** — `sudo apt install python3 python3-venv`.

The first time you dictate, the client builds a virtual environment and installs
its dependencies. That takes a couple of minutes and needs either network access
to a package index or a `wheels/` directory next to the installed `server/`
folder. It happens once; after that, starting a session takes seconds.

### Offline installs

On a machine with no package index, mirror the wheels on a connected machine:

```bash
pip download -d wheels \
  "fastapi>=0.115" "uvicorn[standard]>=0.30" "pyyaml>=6.0" "numpy>=1.26" \
  "faster-whisper>=1.0.3" "onnxruntime>=1.18"
```

Copy the `wheels` directory next to the installed `server` folder:

- macOS — `/Applications/Local Dictation.app/Contents/Resources/wheels`
- Windows — `C:\Program Files\Local Dictation\wheels`

The client uses it with `--no-index` and never touches the network.

---

## 3. Install a speech model

Nothing works without this step. One command:

### macOS

```bash
"/Applications/Local Dictation.app/Contents/Resources/server/scripts/fetch-model.sh" \
  large-v3-turbo --dest ~/Library/Application\ Support/LocalDictation/models
```

### Windows

```powershell
& "C:\Program Files\Local Dictation\server\scripts\fetch-model.ps1" -Model large-v3-turbo
```

### Ubuntu (server install)

```bash
/opt/local-dictation/app/scripts/fetch-model.sh large-v3-turbo --dest /opt/local-dictation/models
```

`large-v3-turbo` is 1.5 GB and is the right starting point on a CPU.
`large-v3` is 2.9 GB and more accurate but several times slower. Full comparison,
offline transfer instructions and checksum verification are in
[model-setup.md](model-setup.md).

---

## 4. Grant permissions

### macOS

Open **System Settings › Privacy & Security › Accessibility** and turn on
**Local Dictation**, then restart the app. Without it macOS will not let any
application register a global shortcut or type into another app — the shortcut
will simply do nothing.

Microphone access is requested the first time you dictate; allow it.

### Windows

No permission step. If the shortcut does not respond, another application is
probably already using `Ctrl+Shift+M`; pick a different key in Settings.

Note that Windows will not let Local Dictation type into a window running as
administrator unless Local Dictation is elevated too.

---

## 5. Confirm it is ready

```bash
"/Applications/Local Dictation.app/Contents/MacOS/local-dictation" --check
```

```powershell
& "C:\Program Files\Local Dictation\local-dictation.exe" --check
```

It prints one line per prerequisite:

```
ok    settings               mode=local language=Korean shortcut=Ctrl + Shift + M
ok    text at the cursor     permitted
ok    microphone             7 device(s) available
ok    server files           /Applications/Local Dictation.app/Contents/Resources/server
ok    python                 /opt/homebrew/bin/python3 (3.12)
ok    model                  /Users/you/Library/Application Support/LocalDictation/models/large-v3-turbo

Everything needed to dictate is in place.
```

Anything marked `FAIL` names what to fix. Then read [usage.md](usage.md).

---

## Installing shared servers (Ubuntu or macOS)

Skip this unless you are running servers other people connect to.

```bash
tar xzf local-dictation-server-0.1.1.tar.gz
```

```bash
cd local-dictation-server-0.1.1 && ./install.sh
```

That needs no privileges: it installs into `~/local-dictation` and links the
command into `~/.local/bin`. **Root is not required to run a speech server**, and
an install you own is the one where `start`, `stop` and `fetch-model` also need
no `sudo`.

| | `./install.sh` | `sudo ./install.sh` |
| --- | --- | --- |
| Prefix | `~/local-dictation` | `/opt/local-dictation` |
| Command linked into | `~/.local/bin` | `/usr/local/bin` |
| Listens on | `127.0.0.1` | `0.0.0.0` |
| TLS | off | on, certificates expected in `$PREFIX/tls` |
| Everyday commands need sudo | no | no — `run/`, `log/` and `models/` are handed to you |

`--prefix /srv/dictation` puts it anywhere you can write. Whichever you choose,
the generated configs point **inside that prefix**, so there are no paths to
edit before the first start.

The installer creates the directory layout, builds a virtual environment,
installs the dependencies and links `local-dictation-server` onto your PATH. It
does **not** register a system service: the management command runs both
language servers as background processes, which is all this needs.

Then install a model (step 3). The configs already name it:

```bash
~/local-dictation/app/scripts/fetch-model.sh large-v3-turbo --dest ~/local-dictation/models
```

Check, then start. `check` opens every file the configs name, so a wrong path is
a message rather than a server that dies seconds later:

```bash
local-dictation-server check all
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

Other commands: `stop`, `restart`, `status`, `logs ko --follow`, `check`. Each
takes `ko`, `en` or `all`.

### TLS

The shipped configs expect certificates under `/opt/local-dictation/tls` and
require client certificates. For a first run on a trusted network, set
`security.tls_certificate`, `tls_private_key` and `client_ca` to `null` and
`require_client_certificate` to `false` in both files. Turn them back on before
anyone relies on this. See [operations.md](operations.md).

### Pointing clients at them

In the client's **Settings** tab, choose **Remote servers**, enter the address
and the two ports, then press **Test connections**. Both LEDs should turn green.

---

## Uninstalling

**macOS** — drag `/Applications/Local Dictation.app` to the Trash. Settings,
logs and the downloaded model stay in
`~/Library/Application Support/LocalDictation`; delete that folder to remove
them.

**Windows** — Settings › Apps › Local Dictation › Uninstall. Per-user data stays
in `%APPDATA%\LocalDictation`.

**Server** — `local-dictation-server stop all`, then delete the install prefix
and the `local-dictation-server` symlink.
