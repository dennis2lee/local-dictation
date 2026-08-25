# Using the client

Put your cursor where the text should go, press the shortcut, speak, press it
again. That is the whole thing.

If you have not installed it yet, start with [client-install.md](client-install.md).

---

## Everyday dictation

1. **Put the cursor where you want the text** — an email, a document, a chat
   box, a form field. Local Dictation types into whatever has focus; it does not
   have a text area of its own.
2. **Pick the language** on the Main tab: Korean or English. The choice decides
   which server the session uses. There is no automatic detection — a Korean
   server transcribes Korean, an English server transcribes English, and both
   stay predictable.
3. **Press `Ctrl+Shift+M`.** The indicator turns green and words start appearing
   at your cursor a moment after you start speaking.
4. **Keep talking.** Words appear a beat behind your voice and then stay put.
   Nothing that has been typed is rewritten, so you can watch the text or not,
   as you prefer.
5. **Press `Ctrl+Shift+M` again.** The indicator turns amber while the accurate
   model does one last pass, then the finished sentence is committed and the
   session ends.

The shortcut works from any application. You do not have to bring Local
Dictation to the front, and you should not: it types into whatever was focused
when you pressed it.

## The window, and the thing in the tray

Closing the window does not quit. Local Dictation stays in the menu bar on
macOS and the notification area on Windows, and the shortcut goes on working —
which is the point of a tool you drive from a shortcut rather than from a
window.

That means it is usually already running. Opening it again from the Start menu,
the Dock, a desktop shortcut or Spotlight brings the window you already have
back to the front; it does not start a second copy. Before 0.1.26 it did, which
put a second icon in the tray, registered the shortcut twice and started a
second speech server — none of which said anything, it just behaved oddly
afterwards.

To actually stop it, use **Quit** in the tray menu. The same menu has **Show**
and **Start / stop dictation**, so a session can be run without the window at
all.

## Why text arrives a beat late

A word is typed once, when the server is sure of it, and then it never moves.

The delay is the cost of that certainty. Whisper revises its guess at the last
few words as more of the sentence arrives, so a word can only be typed once it
has stopped changing — which takes one more pass. What you get in exchange is
that nothing on screen is ever rewritten under you.

The alternative is on the **Typing** tab: **Show words before they settle**
types the unsettled tail as it is guessed and rewrites it whenever it changes.
It looks livelier and it cannot keep up with a fast speaker — see that tab for
why.

If the connection drops or something goes wrong, everything already typed is
kept. You never end up with half a wrong word left behind, and you never lose a
sentence you already watched finish.

## Punctuation and formatting

Whisper punctuates and capitalises from the sound of the sentence, so speak
naturally and it will usually get it right. It does not respond to spoken
commands: saying "comma" types the word "comma". Say the sentence, then fix
punctuation by hand if it matters.

Pausing for about half a second ends the sentence and commits it. That is the
natural rhythm to dictate in: a sentence, a breath, a sentence. Each finished
sentence is followed by a space, so a paragraph dictated in several breaths
reads as a paragraph rather than as one run-on line.

## Status indicator

| Colour | Meaning |
| --- | --- |
| Grey | Stopped. Press the shortcut to start. |
| Amber | Connecting, or finishing the last sentence. |
| Green | Listening. Text is going to your cursor. |
| Red | Something needs your attention; the message says what. |

Settings are locked while the indicator is green or amber. Stop dictating to
change them.

The four colours are the most saturated thing in the window on purpose: nothing
else — not the accent on the buttons, not the active tab — is allowed to be
more colourful than the light that carries the answer.

---

## Settings

Everything here is set once, after installing. It is grouped into tabs, so that
finding one setting does not mean scrolling past all of them:

| Tab | What is on it |
| --- | --- |
| **Server** | Which server to use, its address, and the connection test |
| **Local** | What decodes, and where the models are, for the server this app runs itself |
| **Models** | Which models are installed, which are missing, and a button that fetches them |
| **Advanced** | Python, threads and ports for that same built-in server |
| **Audio** | Microphone, level meter and input gain |
| **Typing** | The shortcut, and when words are typed |
| **Updates** | The installed version, and checking for a newer one |

**Save** sits below the tabs and applies all of them at once, so edits on
several tabs are one save.

### Server

**This computer** (the default) runs the speech server on your own machine. The
first session after launching takes longer while the model loads — around ten
seconds, and the first ever run also builds a Python environment. Later sessions
start immediately because the server stays running.

**Remote servers** connects to servers someone else runs. Enter the address and
the two ports, then press **Test connections**. Two LEDs report each server
separately, so a misconfigured port on one language is obvious.

A common and confusing mistake is swapping the ports. The client checks for it:
if the Korean port answers with an English server, the test says so rather than
letting you dictate Korean into an English model, which produces fluent nonsense
rather than an error.

**Use TLS (wss)** stays off, and the three certificate fields below it stay
empty, unless the server you are connecting to has its own certificate
authority. Every server this project installs listens without TLS, so the
common case is: address, two ports, nothing else. The certificate fields only
appear once the switch is on, because three empty boxes read as three things
left undone.

If the server is on a network you would rather not send audio across in the
clear, the answer is usually an SSH tunnel rather than certificates — the
[server install guide](server-install.md#serving-other-machines) has the
command. Connect to `127.0.0.1` and leave TLS off; SSH is already doing both
jobs.

### Local

**Decode on** chooses the hardware. It appears only where there is more than
one answer, and the choices are whatever this machine could actually run:

| | Where | What it uses |
| --- | --- | --- |
| **CPU** | Everywhere | faster-whisper. The default, and what every install had before this setting existed. |
| **Intel GPU** | Windows, Linux | OpenVINO. For an Intel Arc GPU — a 140V in a Lunar Lake laptop, or a card. |
| **Apple GPU** | macOS | MLX. For Apple Silicon. |

**Each one reads a different model**, and the three are not interchangeable.
The line under the selector says which file the chosen one wants;
[model-setup.md](model-setup.md) has the command to fetch it. Switching the
selector does not move or delete anything, so a machine can hold both and you
change the **Model directory** field to match.

Neither GPU option falls back to the CPU when the hardware is not there. A
missing GPU, driver or plugin is an error at the first attempt to dictate,
naming what was missing — because a GPU backend silently decoding on the CPU
looks exactly like a slow computer, and there is nothing on screen to
distinguish them.

Each backend keeps its own Python environment, so switching to one for the
first time installs its dependencies before the first session. That happens
once.

### Models

Nothing here works without a speech model, and none is included in any
installer — they are gigabytes and carry their own licence. This tab is where
they are installed and where you can see which already are.

Models for the backend chosen on the **Local** tab come first, under a heading
naming it. The rest are listed below, because "what have I already downloaded"
is the other half of the question.

| Colour | What it means |
| --- | --- |
| Green | Installed. |
| Red | This backend has nothing it can decode with. Dictation will not start until one of these arrives. |
| Plain | Not installed, and not needed: an alternative to something you already have, optional, or for another backend. |

Only one red row has to be fixed, not all of them. `large-v3-turbo` and
`large-v3` both serve the CPU — installing either clears it.

**Download** fetches one. The line at the top says which directory they go in:
whichever one **Model directory** on the **Local** tab already names, so a
download lands where this app is looking rather than where a script's default
would have put it.

An interrupted download leaves nothing behind. Files are staged and only moved
into place once the whole set has arrived, so a model is either installed or it
is not — never a directory that looks right and fails to load. A download that
replaces a working model keeps the old one until the new one is complete.

When you download the accurate model for the backend you have chosen, and
**Model directory** was pointing somewhere that backend cannot read, the field
is moved to the new model. It is not saved for you — press **Save**.

The draft model (`base`) is worth having on a CPU and is listed as optional
because it is: it produces only the live text and nothing that is kept. On a
GPU, measure before adding it — see [latency.md](latency.md).

`silero_vad.onnx` decides when you have stopped speaking. Without it the server
falls back to a plain loudness threshold, which is worse but still works.

Everything here can also be done from a terminal with `fetch-model.sh` or
`fetch-model.ps1` — [model-setup.md](model-setup.md) has those, plus offline
transfer and checksum verification. The two write the same files and the same
`SHA256SUMS`, so a model fetched either way verifies with either.

### Audio

Pick a device, then press **Test microphone** and speak. The meter beside it
should light up. Press it again to stop.

**Default system microphone** follows whatever the OS is using, so plugging in a
headset switches automatically. Choosing a specific device pins it; if that
device is later unplugged, dictation reports it rather than silently recording
from something else.

The meter under it is a row of segments: green while you are at a good level,
amber for loud, red for clipping. Watch it while you speak normally — the green
segments with the occasional amber peak is what you want.

**Input level** raises or lowers the signal before anything else sees it. Some
microphones — laptop arrays with noise suppression in front of them, headsets
sitting at a low mixer position — arrive quiet enough that Whisper hears a
whisper, and the decoder has no automatic gain of its own: quiet in is quiet
out, and the transcript pays for it.

Move the slider while watching the meter; it takes effect immediately, so the
meter is the instrument for setting it. It is a plain multiplier, not a
compressor, so it cannot pump or breathe mid-sentence — and it cannot rescue a
signal that was already clipping. If the meter is reaching red before you touch
it, turn the microphone down at the system mixer instead.

### Typing

Choose the modifiers and key for the shortcut. The change takes effect when you
save.

If the shortcut does not respond after saving, something else is already using
it — pick a different key. On macOS, the other possibility is that Accessibility
permission was revoked; the Main tab says so when that happens.

**Show words before they settle**, on the same tab, is off by default.

With it off a word is typed once, when the server is sure of it. Text appears a
beat behind you and never moves.

With it on, the tail Whisper has not settled on yet is typed as it is guessed
and rewritten whenever it changes. It looks livelier, and it cannot keep up with
a fast speaker: neither platform here has a real input method behind it, so
rewriting means backspacing real characters out of your document and typing
them again — for one short sentence, forty-six keystrokes instead of fourteen.
If dictation lags behind your voice, this is the setting.

### Updates

Shows the installed version. **Update** does the whole thing: it looks for a
newer release, and if there is one it downloads it, installs it, and reopens
the app. There is nothing to confirm in between — pressing the button is the
decision. If you are already on the newest release it says so and stops.

The line above the button says where it will look before you press it. By
default that is this project's own releases on github.com.

The download is handed to the operating system's own installer — Installer.app
on macOS, `msiexec` on Windows. Both ask for administrator authorisation
themselves, in their own window; Local Dictation never sees a password. It then
closes, because the application it is running from is exactly what is being
replaced, and reopens once the install finishes.

**update.check_on_start** only checks. It reports what it found and waits for
you to press **Update** — updating and restarting because someone opened the
app would be a different setting, and one nobody turned on.

If the installer will not start, the message says so and the downloaded file is
still in your `Downloads` folder — opening it by hand is the same install.

The download is checked against the `SHA256SUMS` published with that release
before it is kept; a file that does not match is deleted rather than saved. What
that catches is a corrupted or truncated download. It is not a signature: the
trust is HTTPS and the repository itself. A release with no published checksums
is reported and not offered as a download — the link to the release page is
still there, so fetching it by hand stays your decision.

Nothing is sent anywhere. The check is one HTTPS GET for the release list, and
it only happens when you press the button.

**On a managed deployment**, set `update.manifest_url` and `update.public_key`
in `settings.json` — beside `startup.log`, in the folders listed under
[The app does not open at all](#the-app-does-not-open-at-all) — to point at your
own distribution server. Those take
precedence, github.com is never contacted, and the stronger rule applies: the
manifest must carry a valid ed25519 signature and the download must match the
hash inside it. A signature that does not verify is refused outright, not
reported as a warning.

A fork that publishes its own releases sets `update.github_repo` to
`owner/name` instead.

---

## When something goes wrong

### The app does not open at all

**First: look in the tray.** Closing the window leaves the app running there, so
"nothing happens when I open it" is usually the window coming forward somewhere
you are not looking — on another desktop or behind the window you are in. The
tray menu's **Show** brings it back.

If there is nothing in the tray either, a window flashes and is gone, or
nothing appears at all. There is no console to read on
Windows — the app is built without one — so the reason goes to a file instead:

| | |
| --- | --- |
| **macOS** | `~/Library/Application Support/LocalDictation/startup.log` |
| **Windows** | `%APPDATA%\LocalDictation\startup.log` |

It is rewritten each launch and holds one line per start, plus the full stack of
anything that went wrong on the way to the first window. `--check` prints its
path, and works even when the window does not:

```bash
"/Applications/Local Dictation.app/Contents/MacOS/local-dictation" --check
```

```powershell
& "C:\Program Files\Local Dictation\local-dictation.exe" --check
```

### The shortcut does nothing

Run the self-check:

```bash
"/Applications/Local Dictation.app/Contents/MacOS/local-dictation" --check
```

```powershell
& "C:\Program Files\Local Dictation\local-dictation.exe" --check
```

It names the missing piece. The two common answers are macOS Accessibility
permission and a missing model — and `--check` holds the model directory to
whatever **Decode on** is set to, so a model that was fine yesterday reading
`FAIL` today usually means the backend changed and the directory did not.

### Dictation stopped when I clicked somewhere else

That is deliberate. Local Dictation types into whatever window has focus, so a
session that kept running after you switched applications would put the rest of
the sentence into whatever you clicked — a chat box, a search field, or a
password box. When focus moves, the session stops: confirmed text stays where it
was written, and the unconfirmed tail is removed.

Put the cursor back and press the shortcut again.

On macOS this check needs the same Accessibility permission everything else
does. If focus cannot be read, the check is skipped rather than stopping every
session, so the warning above about where text lands applies.

### Nothing is transcribed but the indicator is green

The microphone is probably muted, set to a device that is not picking you up, or
turned down too far. Stop dictating and use **Test microphone** on the
**Audio** tab. If no segments light while you speak, the problem is the
device, not the transcription. If a few light but the meter barely leaves the
left end, raise **Input level** until you are in the green with the odd amber
peak.

### Words appear slowly

A partial should appear about a second after you start speaking. If it takes
several seconds, the machine is decoding slower than you speak.
[latency.md](latency.md) explains why and what to change — usually adding a
small draft model, which cuts first-word latency from around 3.7 seconds to
under one without changing the final text at all.

### It transcribed the wrong language

Check which language is selected on the Main tab, and that the ports on
**Settings → Server** are not swapped. Press **Test connections**, at the top
right of that tab: it reports what each port actually serves.

### Text is duplicated or letters go missing

Check whether **Show words before they settle** is on, on the **Typing** tab. It
is off by default, and with it off nothing is ever rewritten, so this class of
problem does not arise.

With it on, provisional text is replaced by sending backspaces, and an
application with aggressive autocorrect or auto-indent can fight with that — it
is most visible in code editors. Turn it off, or dictate into a plain text field
and paste.

### macOS asks for Accessibility again

It should ask once. Later launches check the answer without showing anything,
and a permission granted to one version now carries over to the next: the app is
signed so that macOS recognises an update as the same application rather than as
a stranger with the same name. Builds before 0.1.23 were not, which is why every
update used to ask again — while the switch in System Settings stayed on, which
is the confusing part. It looks granted and is not.

If it does ask again, the reliable fix is to remove **Local Dictation** from
**System Settings › Privacy & Security › Accessibility** with the **−** button
and let the app ask once more, rather than toggling the existing row.

---

## Privacy

Audio and transcripts stay on the machine that transcribes them. In standalone
mode that is your own computer, and nothing touches the network at all. With
shared servers, audio goes to your organisation's server and no further.

Neither the audio nor the text is written to disk or to any log. Servers record
timings, error codes and session counts — never content. This is the default and
it is enforced in code, not by convention: a logging filter redacts any record
that carries transcript text, and the test suite fails if a full session leaves
recognisable text anywhere in the logs.

**Do not dictate into a password field.** The text is typed like keyboard input,
so it will land there, and a password field is exactly the wrong place for
something that also has to be re-typed as it is revised. Local Dictation cannot
reliably tell a password field from any other text field, so this one is on you.
