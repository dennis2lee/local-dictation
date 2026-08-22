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
   at your cursor about a second after you start speaking.
4. **Keep talking.** Text that has settled stops changing. The tail keeps being
   revised as more of the sentence arrives — that is normal, and it is why the
   most recent few words shift around before they stop.
5. **Press `Ctrl+Shift+M` again.** The indicator turns amber while the accurate
   model does one last pass, then the finished sentence is committed and the
   session ends.

The shortcut works from any application. You do not have to bring Local
Dictation to the front, and you should not: it types into whatever was focused
when you pressed it.

## What the two kinds of text mean

While you speak, what you see is a mix of two things:

- **Settled text** — already committed. It will not change.
- **The tail** — the model's current best guess at the last few words. It gets
  replaced as the sentence continues.

If the connection drops or something goes wrong, the settled text is kept and
the tail is removed. You never end up with half a wrong word left behind, and
you never lose a sentence you already watched finish.

## Punctuation and formatting

Whisper punctuates and capitalises from the sound of the sentence, so speak
naturally and it will usually get it right. It does not respond to spoken
commands: saying "comma" types the word "comma". Say the sentence, then fix
punctuation by hand if it matters.

Pausing for about half a second ends the sentence and commits it. That is the
natural rhythm to dictate in: a sentence, a breath, a sentence.

## Status indicator

| Colour | Meaning |
| --- | --- |
| Grey | Stopped. Press the shortcut to start. |
| Amber | Connecting, or finishing the last sentence. |
| Green | Listening. Text is going to your cursor. |
| Red | Something needs your attention; the message says what. |

Settings are locked while the indicator is green or amber. Stop dictating to
change them.

---

## Settings

Everything here is set once, after installing.

### Servers

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

### Microphone

Pick a device, then press **Test microphone** and speak. The bar should move.
Press it again to stop.

**Default system microphone** follows whatever the OS is using, so plugging in a
headset switches automatically. Choosing a specific device pins it; if that
device is later unplugged, dictation reports it rather than silently recording
from something else.

### Shortcut

Choose the modifiers and key. The change takes effect when you save.

If the shortcut does not respond after saving, something else is already using
it — pick a different key. On macOS, the other possibility is that Accessibility
permission was revoked; the Main tab says so when that happens.

### Software update

Shows the installed version. **Check for updates** looks for a newer one, and
the section header line tells you where it will look before you press it.

By default that is this project's own releases on github.com. When a newer one
exists, the version and a link to its notes appear, along with **Download and
install**, showing how large it is.

Pressing it downloads the installer, checks it, and hands it to the operating
system's own installer — Installer.app on macOS, `msiexec` on Windows. Both ask
for administrator authorisation themselves, in their own window; Local
Dictation never sees a password. It then closes, because the application it is
running from is exactly what is being replaced, and reopens once the install
finishes.

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

### Typing

**Show words before they settle**, under Shortcut and typing, is off.

With it off a word is typed once, when the server is sure of it. Text appears a
beat behind you and never moves.

With it on, the tail Whisper has not settled on yet is typed as it is guessed
and rewritten whenever it changes. It looks livelier, and it cannot keep up with
a fast speaker: neither platform here has a real input method behind it, so
rewriting means backspacing real characters out of your document and typing
them again — for one short sentence, forty-six keystrokes instead of fourteen.
If dictation lags behind your voice, this is the setting.

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

Nothing appears, or a window flashes and is gone. There is no console to read on
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
permission and a missing model.

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

The microphone is probably muted or set to a device that is not picking you up.
Stop dictating and use **Test microphone** in Settings — if the bar does not
move, the problem is the device, not the transcription.

### Words appear slowly

A partial should appear about a second after you start speaking. If it takes
several seconds, the machine is decoding slower than you speak.
[latency.md](latency.md) explains why and what to change — usually adding a
small draft model, which cuts first-word latency from around 3.7 seconds to
under one without changing the final text at all.

### It transcribed the wrong language

Check which language is selected on the Main tab, and that the ports in Settings
are not swapped. Press **Test connections**: it reports what each port actually
serves.

### Text is duplicated or letters go missing

Local Dictation replaces provisional text by sending backspaces. An application
with aggressive autocorrect or auto-indent can fight with that. It is most
visible in code editors. If a particular application misbehaves, dictate into a
plain text field and paste.

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
