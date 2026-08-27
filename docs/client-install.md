# Installing the client

The desktop app: macOS and Windows. For a speech server other machines connect
to, see [server-install.md](server-install.md) instead — or as well.

Local Dictation has two halves, and which of them you install depends on the
shape you want:

| | What runs where | Install |
| --- | --- | --- |
| **Standalone** | The client starts its own speech server on your own machine. Nothing leaves it. | This page |
| **Shared servers** | One or two machines run the speech servers; laptops connect to them. | [server-install.md](server-install.md), then this page |

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
sudo installer -pkg LocalDictation-0.1.29.pkg -target /
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
msiexec /i LocalDictation-0.1.29-x64.msi /qn /l*v install.log
```

The app lands in `/Applications` and nowhere else. If you have an older copy
somewhere — a build output, a second volume — remove it first and forget the old
receipt, or macOS may keep upgrading that one instead:

```bash
sudo pkgutil --forget com.local-dictation.client
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
  "onnxruntime>=1.18" "faster-whisper>=1.0.3"
```

Copy the `wheels` directory next to the installed `server` folder:

- macOS — `/Applications/Local Dictation.app/Contents/Resources/wheels`
- Windows — `C:\Program Files\Local Dictation\wheels`

The client uses it with `--no-index` and never touches the network.

**One directory per backend.** Each backend keeps its own Python environment,
so each looks for its own wheels beside the `server` folder — `wheels` for CPU,
`wheels-openvino` for Intel GPU, `wheels-mlx` for Apple GPU. Only the last
package on the line differs:

| Backend | Directory | Instead of `faster-whisper` |
| --- | --- | --- |
| CPU | `wheels` | `faster-whisper>=1.0.3` |
| Intel GPU | `wheels-openvino` | `openvino-genai>=2025.0` |
| Apple GPU | `wheels-mlx` | `mlx-whisper>=0.4` |

Mirror the one you will use. They are kept apart because OpenVINO and
CTranslate2 each bring their own native runtime, and resolving both into a
single environment is how an install that worked yesterday fails to import
today.

---

## 3. Install a speech model

Nothing works without this step, and the easiest place to do it is inside the
app: **Settings → Models** lists every model, marks the ones this machine still
needs in red, and downloads them. It also says which directory they go in, so
nothing has to be pointed at afterwards.

The rest of this section is the same job from a terminal, which is what an
offline or scripted install wants:

### macOS

```bash
"/Applications/Local Dictation.app/Contents/Resources/server/scripts/fetch-model.sh" \
  large-v3-turbo --dest ~/Library/Application\ Support/LocalDictation/models
```

### Windows

```powershell
& "C:\Program Files\Local Dictation\server\scripts\fetch-model.ps1" -Model large-v3-turbo
```

On a machine running a shared server, the model goes beside that install
instead — [server-install.md](server-install.md) has the command.

`large-v3-turbo` is 1.5 GB and is the right starting point on a CPU.
`large-v3` is 2.9 GB and more accurate but several times slower.

**If the machine has a GPU, fetch that GPU's conversion instead** — the formats
are not interchangeable, and the CPU one cannot be loaded onto a GPU:

| Hardware | Model | Then set |
| --- | --- | --- |
| Apple Silicon | `large-v3-turbo-mlx` | Settings → Local → Decode on → **Apple GPU** |
| Intel Arc, Iris Xe | `large-v3-turbo-openvino-int8` | Settings → Local → Decode on → **Intel GPU** |

Full comparison, offline transfer instructions and checksum verification are in
[model-setup.md](model-setup.md).

---

## 4. Grant permissions

### macOS

The first launch asks for Accessibility and offers to open the pane. Turn on
**Local Dictation** there, then restart the app. Without it macOS will not let
any application register a global shortcut or type into another app — the
shortcut will simply do nothing.

It asks once. Later launches check the answer without showing anything, and
the grant now survives updates: the app is signed so that macOS recognises the
next version as the same application rather than as a stranger with the same
name.

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

Anything marked `FAIL` names what to fix. Then read [client-usage.md](client-usage.md).

One thing worth knowing before the first launch: **closing the window does not
quit.** The app stays in the menu bar on macOS and the notification area on
Windows so the shortcut keeps working, and opening it again brings that window
back rather than starting a second copy. **Quit** in the tray menu is how it
stops.

---

## Installing shared servers

Its own guide: [server-install.md](server-install.md). It covers where the
install goes, why it needs no root, the model, TLS, and what to check when a
server will not start.

Once they are running, point this client at them: **Settings → Server**,
**Remote servers**, the address and the two ports, then **Test connections**,
which sits at the top right of that tab. Both LEDs should turn green.

The ports default to 8765 for Korean and 8766 for English. If whoever runs the
servers changed them, these two fields are where the client has to be told —
a mismatch shows as a server it cannot reach.

In standalone mode the client picks its own free ports and there is nothing to
set. **Settings → Advanced** has port fields anyway, left empty; fill them
in only if something on your machine needs the numbers to be predictable.

---

## Uninstalling

**macOS** — drag `/Applications/Local Dictation.app` to the Trash. Settings,
logs and the downloaded model stay in
`~/Library/Application Support/LocalDictation`; delete that folder to remove
them.

**Windows** — Settings › Apps › Local Dictation › Uninstall. Per-user data stays
in `%APPDATA%\LocalDictation`.

**Server** — [server-install.md](server-install.md#uninstalling).
