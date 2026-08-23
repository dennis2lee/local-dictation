package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/audio"
	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/dial"
	"github.com/dennis2lee/local-dictation/client/internal/hotkey"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
	"github.com/dennis2lee/local-dictation/client/internal/update"
)

// settingsTab holds everything touched once, after installing.
type settingsTab struct {
	app *App

	mode *widget.RadioGroup

	// Remote mode
	host        *widget.Entry
	koreanPort  *widget.Entry
	englishPort *widget.Entry
	useTLS      *widget.Check
	caCert      *widget.Entry
	clientCert  *widget.Entry
	clientKey   *widget.Entry
	tlsBox      *fyne.Container
	remoteBox   *fyne.Container

	// Local mode
	modelPath        *widget.Entry
	draftPath        *widget.Entry
	vadPath          *widget.Entry
	pythonPath       *widget.Entry
	cpuThreads       *widget.Entry
	localKoreanPort  *widget.Entry
	localEnglishPort *widget.Entry
	localBox         *fyne.Container
	localState       *widget.Label

	koreanLED  *led
	englishLED *led

	// Microphone
	microphone  *widget.Select
	devices     []audio.Device
	levelBar    *levelMeter
	gain        *widget.Slider
	gainLabel   *widget.Label
	testButton  *widget.Button
	testStop    chan struct{}
	textAdapter *widget.Label

	// Shortcut and typing
	modifierChecks map[string]*widget.Check
	shortcutKey    *widget.Select
	livePreview    *widget.Check

	// Update
	updateStatus *widget.Label
	updateButton *widget.Button
	// offered is the last check's result, which the download button acts on.
	// Both live on the Fyne goroutine and nothing else reads it.
	offered update.Result
	// Two seams, and only tests move them: where a check goes, and whether the
	// work happens on another goroutine. A test that has to poll a widget for
	// a background goroutine's answer is a test that races with it.
	newSource  func(config.Config) update.Source
	background func(func())
	// A third, for the same reason: measuring this tab means building all of
	// it, and enumerating real capture devices in a test asks macOS for the
	// microphone — which is a permission prompt during go test.
	listDevices func() ([]audio.Device, error)
	// And the two ends of the update itself. One press now runs the whole
	// thing, so testing that it does means standing in for the parts that
	// reach the network and replace the running application.
	fetch func(context.Context, update.Artifact) (string, error)
	apply func(saved, version string)

	saveButton *widget.Button
	message    *widget.Label
	groups     *container.AppTabs
	actions    fyne.CanvasObject
}

func newSettingsTab(app *App) *settingsTab {
	settings := app.Settings()
	tab := &settingsTab{app: app, modifierChecks: map[string]*widget.Check{}}

	tab.message = widget.NewLabel("")
	tab.message.Wrapping = fyne.TextWrapWord
	// Nothing to say yet, and a blank line still occupies its height. See say.
	tab.message.Hide()

	// One group per tab, rather than one column with all of them in it.
	//
	// Stacked, these came to about 1335px — more than twice the window, so
	// every setting sat behind a scroll past every other setting, and the
	// window had to be tall to make that bearable. Split, the tallest group is
	// around a third of that, and the window is sized to the tallest group
	// instead of to the sum.
	tab.groups = container.NewAppTabs(
		container.NewTabItem("Server", group(tab.buildServerSection(settings))),
		container.NewTabItem("Local server", group(tab.buildLocalServerSection(settings))),
		container.NewTabItem("Advanced", group(tab.buildAdvancedSection(settings))),
		container.NewTabItem("Microphone", group(tab.buildMicrophoneSection(settings))),
		container.NewTabItem("Typing", group(tab.buildShortcutSection(settings))),
		container.NewTabItem("Updates", group(tab.buildUpdateSection(settings))),
	)
	tab.actions = tab.buildActions()
	return tab
}

func (s *settingsTab) content() fyne.CanvasObject {
	// Save sits below the groups rather than inside one, because it saves all
	// of them at once. On a single group it would look like it saved that
	// group, and it would scroll out of reach of the others.
	footer := inset(container.NewVBox(s.actions, s.message))
	return container.NewBorder(nil, footer, nil, nil, s.groups)
}

// -- servers ---------------------------------------------------------------

func (s *settingsTab) buildServerSection(settings config.Config) fyne.CanvasObject {
	// Attached after the initial value, for the same reason as the language
	// radio: onModeChanged shows and hides localBox and remoteBox, and neither
	// exists until further down this function.
	s.mode = widget.NewRadioGroup([]string{"This computer", "Remote servers"}, nil)
	s.mode.Horizontal = true
	s.mode.SetSelected(modeLabel(settings.Mode))

	s.host = entry(settings.Remote.Host, "dictation.internal")
	s.koreanPort = entry(portText(settings.Remote.KoreanPort), "8765")
	s.englishPort = entry(portText(settings.Remote.EnglishPort), "8766")
	s.caCert = entry(settings.Remote.TLS.CACertificate, "/path/to/internal-ca.crt")
	s.clientCert = entry(settings.Remote.TLS.ClientCertificate, "optional, for mTLS")
	s.clientKey = entry(settings.Remote.TLS.ClientKey, "optional, for mTLS")
	s.useTLS = widget.NewCheck("Use TLS (wss) — for a server with certificates", nil)
	s.useTLS.SetChecked(settings.Remote.TLS.Enabled)

	// These three belong to a managed deployment with an internal CA. On a home
	// network there is nothing to put in them, and showing them anyway reads as
	// three more things to work out before this will connect. They appear when
	// TLS does.
	s.tlsBox = container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("CA certificate", s.caCert),
			widget.NewFormItem("Client certificate", s.clientCert),
			widget.NewFormItem("Client key", s.clientKey),
		),
	)
	s.remoteBox = container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Server address", s.host),
			widget.NewFormItem("Korean port", s.koreanPort),
			widget.NewFormItem("English port", s.englishPort),
		),
		s.useTLS,
		s.tlsBox,
	)
	// Attached after tlsBox exists, for the same reason the mode radio is.
	s.onTLSChanged(s.useTLS.Checked)
	s.useTLS.OnChanged = s.onTLSChanged

	s.koreanLED = newLED("Korean: not checked")
	s.englishLED = newLED("English: not checked")

	// Attached after remoteBox exists, and it is the only box this toggles
	// now: the local server's fields have a tab of their own, so there is
	// nothing to hide there.
	s.onModeChanged(s.mode.Selected)
	s.mode.OnChanged = s.onModeChanged

	// Test connections shares the mode's row rather than taking one of its
	// own. It is the only row on this tab whose width is known and fixed —
	// two radio labels — so nothing here can grow into the button, and the
	// row it saves is what kept this tab from fitting the window.
	test := primaryButton(
		widget.NewButtonWithIcon("Test connections", theme.ViewRefreshIcon(), s.onTestConnections))

	return container.NewVBox(
		container.NewBorder(nil, nil, s.mode, test),
		s.remoteBox,
		s.koreanLED.Object(),
		s.englishLED.Object(),
	)
}

// -- the built-in server ---------------------------------------------------

// buildLocalServerSection is everything about the server this app starts for
// itself. It was the tallest thing in Settings by some way — seven fields and
// two paragraphs — which is most of why the window had to be as tall as it was.
func (s *settingsTab) buildLocalServerSection(settings config.Config) fyne.CanvasObject {
	s.modelPath = entry(settings.Local.ModelPath, config.DefaultModelPath())
	s.draftPath = entry(settings.Local.DraftModelPath,
		"optional: a small model (base) for sub-second partials")
	s.vadPath = entry(settings.Local.VadModelPath, "optional: silero_vad.onnx")
	s.pythonPath = entry(settings.Local.PythonPath, "optional: leave empty to find Python automatically")
	s.cpuThreads = entry(threadsText(settings.Local.CPUThreads), "0 = let the decoder choose")
	// Empty means 0, which means "pick a free one". Something else on the
	// machine holding 8765 is exactly why the port a session failed on needs to
	// be reachable from here — the failure already says to come and change it.
	s.localKoreanPort = entry(portText(settings.Local.KoreanPort), "empty = pick a free port")
	s.localEnglishPort = entry(portText(settings.Local.EnglishPort), "empty = pick a free port")
	s.localState = widget.NewLabel("The server starts the first time you dictate.")
	s.localState.Wrapping = fyne.TextWrapWord

	s.localBox = container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Model directory", s.modelPath),
			widget.NewFormItem("Draft model directory", s.draftPath),
			widget.NewFormItem("Silero VAD file", s.vadPath),
		),
		widget.NewLabel("Models are not installed with the app. See docs/model-setup.md."),
		s.localState,
	)

	// These used to be hidden outright in Remote mode. On their own tab that
	// would leave a blank one, so they stay and say when they apply — which
	// also lets someone set the built-in server up before switching to it.
	return container.NewVBox(
		inlineCaption("Used when Server is set to \"This computer\"."),
		s.localBox,
	)
}

// -- the built-in server, the parts nobody touches -------------------------

// buildAdvancedSection is the rest of the local server: which Python runs it,
// how many threads it decodes on, and which ports it listens on.
//
// They are separated from the model paths because they are answered by their
// defaults. Someone setting this up needs the model directory and nothing on
// this tab, and four fields that are almost always right made the group they
// were in the tallest thing in the window.
func (s *settingsTab) buildAdvancedSection(settings config.Config) fyne.CanvasObject {
	return container.NewVBox(
		inlineCaption("For the built-in server. The defaults suit most machines."),
		widget.NewForm(
			widget.NewFormItem("Python", s.pythonPath),
			widget.NewFormItem("CPU threads", s.cpuThreads),
			widget.NewFormItem("Korean port", s.localKoreanPort),
			widget.NewFormItem("English port", s.localEnglishPort),
		),
	)
}

// onTLSChanged shows the certificate fields only when they can matter.
func (s *settingsTab) onTLSChanged(enabled bool) {
	if enabled {
		s.tlsBox.Show()
		return
	}
	s.tlsBox.Hide()
}

// onModeChanged shows the remote server's fields only when one is in use.
//
// It no longer hides the local server's, because those have their own tab and
// hiding them would empty it.
func (s *settingsTab) onModeChanged(label string) {
	if modeFromLabel(label) == config.ModeLocal {
		s.remoteBox.Hide()
		return
	}
	s.remoteBox.Show()
}

func (s *settingsTab) onTestConnections() {
	s.koreanLED.Set("amber", "Korean: checking…")
	s.englishLED.Set("amber", "English: checking…")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, language := range []protocol.Language{protocol.Korean, protocol.English} {
			health := s.app.dialer.TestConnection(ctx, language)
			indicator := s.koreanLED
			if language == protocol.English {
				indicator = s.englishLED
			}
			fyne.Do(func() { s.showHealth(indicator, health) })
		}
	}()
}

func (s *settingsTab) showHealth(indicator *led, health dial.Health) {
	name := "Korean"
	if health.Language == protocol.English {
		name = "English"
	}
	if health.OK {
		indicator.Set("green", fmt.Sprintf("%s: %s", name, health.Detail))
		return
	}
	indicator.Set("red", fmt.Sprintf("%s: %s", name, health.Detail))
}

// -- microphone ------------------------------------------------------------

func (s *settingsTab) buildMicrophoneSection(settings config.Config) fyne.CanvasObject {
	s.microphone = widget.NewSelect(nil, nil)
	s.levelBar = newLevelMeter()

	// A slider rather than a number: the useful range is small, the right
	// value is found by watching the meter rather than by knowing it, and a
	// text field would invite typing 40.
	s.gainLabel = widget.NewLabel("")
	s.gain = widget.NewSlider(audio.MinGain, audio.MaxGain)
	s.gain.Step = 0.1
	s.gain.SetValue(settings.Audio.InputGain())
	s.showGain(settings.Audio.InputGain())
	s.gain.OnChanged = func(value float64) {
		s.showGain(value)
		// Live, so the meter under it responds while the slider is moving —
		// which is the only way to choose a value. Saved with everything else.
		if s.app.capture != nil {
			s.app.capture.SetGain(value)
		}
	}
	s.testButton = widget.NewButtonWithIcon("Test microphone", theme.VolumeUpIcon(), s.onTestMicrophone)

	available, reason := s.app.textAdapterAvailability()
	s.textAdapter = widget.NewLabel(reason)
	s.textAdapter.Wrapping = fyne.TextWrapWord
	if available {
		s.textAdapter.Hide()
	}

	s.refreshDevices(settings.Audio.DeviceID)

	return container.NewVBox(
		s.microphone,
		container.NewBorder(nil, nil, nil, s.testButton, s.levelBar.Object()),
		container.NewBorder(nil, nil, inlineCaption("Input level"), fixedWidth(s.gainLabel, widestGainLabel()), s.gain),
		caption("Raise this if the meter barely moves when you speak normally. "+
			"Aim for the green segments with the odd amber peak; anything reaching "+
			"red is clipping, which the decoder hears as distortion."),
		widget.NewButtonWithIcon("Refresh device list", theme.ViewRefreshIcon(), func() {
			s.refreshDevices(s.app.Settings().Audio.DeviceID)
		}),
		s.textAdapter,
	)
}

// availableDevices is the capture backend, or whatever a test put in its place.
func (s *settingsTab) availableDevices() ([]audio.Device, error) {
	if s.listDevices != nil {
		return s.listDevices()
	}
	return s.app.capture.Devices()
}

func (s *settingsTab) refreshDevices(selectedID string) {
	devices, err := s.availableDevices()
	s.devices = devices

	names := make([]string, 0, len(devices))
	selectedName := ""
	for _, device := range devices {
		names = append(names, device.Name)
		if device.ID == selectedID {
			selectedName = device.Name
		}
	}
	s.microphone.Options = names
	if selectedName == "" && len(names) > 0 {
		selectedName = names[0]
	}
	s.microphone.SetSelected(selectedName)
	s.microphone.Refresh()

	if err != nil {
		s.say(fmt.Sprintf("Could not list microphones: %v", err))
	}
}

func (s *settingsTab) selectedDevice() audio.Device {
	for _, device := range s.devices {
		if device.Name == s.microphone.Selected {
			return device
		}
	}
	return audio.SystemDefault()
}

// onTestMicrophone runs a level meter until pressed again.
//
// It refuses while a session is live: the device is already open, and opening
// it twice fails on Windows and silently steals the stream on macOS.
func (s *settingsTab) onTestMicrophone() {
	if s.testStop != nil {
		close(s.testStop)
		s.testStop = nil
		_ = s.app.capture.Stop()
		s.testButton.SetText("Test microphone")
		s.levelBar.Set(0)
		return
	}

	if !s.app.controller.State().AcceptsSettingsChanges() {
		s.say("Stop dictation before testing the microphone.")
		return
	}

	device := s.selectedDevice()
	if err := s.app.capture.Start(device.ID, func([]byte) {}); err != nil {
		s.say(fmt.Sprintf("Could not open %s: %v", device.Name, err))
		return
	}

	stop := make(chan struct{})
	s.testStop = stop
	s.testButton.SetText("Stop test")
	s.say("Speak normally — the bar should move.")

	go func() {
		ticker := time.NewTicker(60 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				peak, _ := s.app.capture.Meter().Level()
				fyne.Do(func() { s.levelBar.Set(peak) })
			}
		}
	}()
}

// -- shortcut --------------------------------------------------------------

func (s *settingsTab) buildShortcutSection(settings config.Config) fyne.CanvasObject {
	selected := map[string]bool{}
	for _, modifier := range settings.Hotkey.Modifiers {
		selected[modifier] = true
	}

	row := container.NewHBox()
	for _, name := range hotkey.AvailableModifiers() {
		check := widget.NewCheck(name, nil)
		check.SetChecked(selected[name])
		s.modifierChecks[name] = check
		row.Add(check)
	}

	keys := hotkey.AvailableKeys()
	sortStrings(keys)
	s.shortcutKey = widget.NewSelect(keys, nil)
	s.shortcutKey.SetSelected(settings.Hotkey.Key)

	s.livePreview = widget.NewCheck("Show words before they settle", nil)
	s.livePreview.SetChecked(settings.Input.LivePreview)

	return container.NewVBox(
		row,
		s.shortcutKey,
		caption("The shortcut works in any application. It takes effect when you save."),
		s.livePreview,
		caption("Off, a word is typed once, when the server is sure of it. On, the "+
			"unsettled tail is typed as it is guessed and rewritten whenever it "+
			"changes — which looks livelier and cannot keep up with a fast speaker, "+
			"because rewriting means backspacing real characters out of your document."),
	)
}

func (s *settingsTab) shortcutFromForm() config.Hotkey {
	modifiers := make([]string, 0, len(s.modifierChecks))
	for _, name := range hotkey.AvailableModifiers() {
		if check, ok := s.modifierChecks[name]; ok && check.Checked {
			modifiers = append(modifiers, name)
		}
	}
	return config.Hotkey{Modifiers: modifiers, Key: s.shortcutKey.Selected}
}

// widestGainLabel is how much room to reserve for the reading beside the
// slider.
//
// Without a reservation the label is as wide as its text, so "+18 dB" makes it
// 11px wider than "-6 dB" and the slider gives up that width as it is dragged
// louder — the thumb slides out from under the pointer holding it. Measured
// rather than hardcoded, since the width is the theme's font at the user's
// display scale, neither of which this file knows.
func widestGainLabel() float32 {
	widest := float32(0)
	for _, multiplier := range []float64{audio.MinGain, audio.MaxGain} {
		sample := widget.NewLabel(gainText(multiplier))
		if width := sample.MinSize().Width; width > widest {
			widest = width
		}
	}
	return widest
}

// gainText renders the multiplier the way the label shows it.
func gainText(multiplier float64) string {
	return fmt.Sprintf("%+.0f dB", 20*math.Log10(multiplier))
}

// showGain writes the multiplier as decibels, which is the unit anyone who has
// touched an audio control before already reads.
func (s *settingsTab) showGain(multiplier float64) {
	s.gainLabel.SetText(gainText(multiplier))
}

// -- update ----------------------------------------------------------------

func (s *settingsTab) buildUpdateSection(settings config.Config) fyne.CanvasObject {
	s.updateStatus = widget.NewLabel(fmt.Sprintf(
		"Not checked yet. Updates come from %s.", s.updateSource(settings).Describe()))
	s.updateStatus.Wrapping = fyne.TextWrapWord

	// One button for the whole thing: check, download, install, reopen.
	//
	// It used to take two — one to look, one to accept what was found — which
	// meant an update someone had already decided to install still waited on a
	// second press. The button says what it does, so pressing it is the
	// decision; there is no second one to make.
	s.updateButton = primaryButton(
		widget.NewButtonWithIcon("Update", theme.DownloadIcon(), s.onUpdate))

	return container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Local Dictation %s", s.app.options.Version)),
		s.updateStatus,
		container.NewHBox(s.updateButton),
	)
}

// updateSource is where a check goes: an internal distribution server when the
// settings file names one, this project's own GitHub releases otherwise.
//
// The button used to be disabled outright without an internal server — which
// meant everyone installing from the published packages had an update feature
// whose only output was that it was not configured.
func (s *settingsTab) updateSource(settings config.Config) update.Source {
	if s.newSource != nil {
		return s.newSource(settings)
	}
	return update.SourceFor(
		settings.Update.ManifestURL,
		settings.Update.PublicKey,
		settings.Update.GitHubRepo,
		s.app.options.Version,
	)
}

// onUpdate takes the app from "is there a newer one" to running it. It is what
// the Update button does.
//
// Every step reports what it is doing, because the last one closes the window:
// an application that quits without having said why has gone wrong as far as
// anyone watching is concerned.
func (s *settingsTab) onUpdate() { s.lookForUpdate(installIfNewer) }

// checkOnStart is the same check without the install.
//
// update.check_on_start says check, and that is all it may do. Installing a
// new version and restarting because someone opened the app would be a
// different setting, one nobody turned on.
func (s *settingsTab) checkOnStart() { s.lookForUpdate(reportOnly) }

// What lookForUpdate does when it finds a newer release.
const (
	installIfNewer = true
	reportOnly     = false
)

func (s *settingsTab) lookForUpdate(install bool) {
	source := s.updateSource(s.app.Settings())
	s.updateStatus.SetText(fmt.Sprintf("Asking %s…", source.Describe()))
	s.updateButton.Disable()

	s.away(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		result, err := source.Check(ctx)

		fyne.Do(func() {
			s.offered = result
			switch {
			case err != nil:
				s.updateButton.Enable()
				s.updateStatus.SetText(fmt.Sprintf("Update check failed: %v", err))
			case !result.Newer:
				s.updateButton.Enable()
				s.updateStatus.SetText(fmt.Sprintf(
					"Local Dictation %s is the newest release.", result.Current))
			case !install:
				s.updateButton.Enable()
				s.updateStatus.SetText(offerText(result) + "\nPress Update to install it.")
			default:
				s.downloadAndInstall(result)
			}
		})
	})
}

// downloadAndInstall fetches the offered artifact and hands it to the platform
// installer. The button stays disabled throughout: there is nothing else to
// press, and the process is about to end.
func (s *settingsTab) downloadAndInstall(offered update.Result) {
	s.updateStatus.SetText(fmt.Sprintf("%s\nDownloading %s…",
		offerText(offered), humanSize(offered.Artifact.Size)))

	s.away(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		saved, err := s.fetchArtifact(ctx, offered.Artifact)

		fyne.Do(func() {
			if err != nil {
				s.updateButton.Enable()
				s.updateStatus.SetText(fmt.Sprintf("Download failed: %v", err))
				return
			}
			s.applyDownload(saved, offered.Available)
		})
	})
}

func (s *settingsTab) fetchArtifact(ctx context.Context, artifact update.Artifact) (string, error) {
	if s.fetch != nil {
		return s.fetch(ctx, artifact)
	}
	return update.Download(ctx, artifact, downloadDir(s.app.options.StateDir), nil)
}

func (s *settingsTab) applyDownload(saved, version string) {
	if s.apply != nil {
		s.apply(saved, version)
		return
	}
	s.installDownloaded(saved, version)
}

func (s *settingsTab) installDownloaded(saved, version string) {
	if err := update.Install(saved, applicationPath()); err != nil {
		// Nothing has been lost: the file is downloaded and verified, and
		// opening it by hand is the same install.
		s.updateStatus.SetText(fmt.Sprintf(
			"Downloaded %s, but the installer would not start: %v\n"+
				"Open %s yourself to finish.", version, err, saved))
		s.updateButton.Enable()
		return
	}
	s.updateStatus.SetText(fmt.Sprintf(
		"Installing %s. Local Dictation will close and reopen when it is done.",
		version))
	// Give the sentence above a moment to be read, then go. The installer is
	// already running and does not depend on this process staying alive.
	s.away(func() {
		time.Sleep(2 * time.Second)
		fyne.Do(s.app.Quit)
	})
}

// applicationPath is the bundle or executable to reopen after an install.
func applicationPath() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	// macOS: .../Local Dictation.app/Contents/MacOS/local-dictation, and it is
	// the bundle that `open` wants, not the executable inside it.
	if index := strings.Index(executable, ".app/Contents/MacOS/"); index >= 0 {
		return executable[:index+len(".app")]
	}
	return executable
}

// away runs the network part of an update off the Fyne goroutine.
func (s *settingsTab) away(work func()) {
	if s.background != nil {
		s.background(work)
		return
	}
	go work()
}

// offerText is what the window says about a release it found.
func offerText(result update.Result) string {
	text := fmt.Sprintf("Version %s is available.", result.Available)
	if notes := strings.TrimSpace(result.Notes); notes != "" {
		text += " " + notes
	}
	if result.Page != "" {
		text += "\n" + result.Page
	}
	return text
}

// downloadDir is where an installer lands: the Downloads folder, because that
// is where someone will look for it and where their browser would have put it.
// A machine without one falls back to the application's own state directory.
func downloadDir(stateDir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		downloads := filepath.Join(home, "Downloads")
		if info, err := os.Stat(downloads); err == nil && info.IsDir() {
			return downloads
		}
	}
	return filepath.Join(stateDir, "updates")
}

func installerName(url string) string {
	if url == "" {
		return "the installer"
	}
	return path.Base(url)
}

// humanSize is for a button label, where "24 MB" is the whole of what someone
// needs and 25341952 is not.
func humanSize(bytes int64) string {
	switch {
	case bytes <= 0:
		return "unknown size"
	case bytes < 1<<20:
		return fmt.Sprintf("%d KB", bytes>>10)
	default:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	}
}

// -- actions ---------------------------------------------------------------

func (s *settingsTab) buildActions() fyne.CanvasObject {
	s.saveButton = widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), s.onSave)
	s.saveButton.Importance = widget.HighImportance
	return container.NewHBox(s.saveButton)
}

// say puts a line under the Save button, and gives the space back when there
// is nothing to say.
//
// The label is one wrapped line of permanent blank otherwise, below every
// group on every tab, for the sake of the few seconds after a save.
func (s *settingsTab) say(text string) {
	s.message.SetText(text)
	if text == "" {
		s.message.Hide()
		return
	}
	s.message.Show()
}

func (s *settingsTab) onSave() {
	settings := s.app.Settings()

	settings.Mode = modeFromLabel(s.mode.Selected)
	settings.Remote.Host = s.host.Text
	settings.Remote.KoreanPort = parsePort(s.koreanPort.Text)
	settings.Remote.EnglishPort = parsePort(s.englishPort.Text)
	settings.Remote.TLS.Enabled = s.useTLS.Checked
	settings.Remote.TLS.CACertificate = s.caCert.Text
	settings.Remote.TLS.ClientCertificate = s.clientCert.Text
	settings.Remote.TLS.ClientKey = s.clientKey.Text

	settings.Local.ModelPath = s.modelPath.Text
	settings.Local.DraftModelPath = s.draftPath.Text
	settings.Local.VadModelPath = s.vadPath.Text
	settings.Local.PythonPath = s.pythonPath.Text
	settings.Local.CPUThreads = parsePort(s.cpuThreads.Text)
	settings.Local.KoreanPort = parsePort(s.localKoreanPort.Text)
	settings.Local.EnglishPort = parsePort(s.localEnglishPort.Text)

	device := s.selectedDevice()
	settings.Audio.DeviceID, settings.Audio.DeviceName = device.ID, device.Name
	settings.Hotkey = s.shortcutFromForm()
	settings.Input.LivePreview = s.livePreview.Checked
	settings.Audio.Gain = audio.ClampGain(s.gain.Value)

	if err := s.app.ApplySettings(settings); err != nil {
		s.say(err.Error())
		dialog.ShowError(err, s.app.window)
		return
	}
	s.say("Settings saved.")
}

// setEditable locks the tab while dictation is running, matching the plan's
// rule that settings change only in IDLE.
func (s *settingsTab) setEditable(editable bool) {
	widgets := []fyne.Disableable{
		s.mode, s.host, s.koreanPort, s.englishPort, s.useTLS, s.caCert,
		s.clientCert, s.clientKey, s.modelPath, s.draftPath, s.vadPath, s.pythonPath,
		s.cpuThreads, s.localKoreanPort, s.localEnglishPort,
		s.microphone, s.testButton, s.shortcutKey, s.saveButton,
	}
	for _, check := range s.modifierChecks {
		widgets = append(widgets, check)
	}
	for _, item := range widgets {
		if editable {
			item.Enable()
		} else {
			item.Disable()
		}
	}
}

// -- helpers ---------------------------------------------------------------

func entry(value, placeholder string) *widget.Entry {
	field := widget.NewEntry()
	field.SetPlaceHolder(placeholder)
	field.SetText(value)
	return field
}

func portText(port int) string {
	if port == 0 {
		return ""
	}
	return strconv.Itoa(port)
}

func threadsText(threads int) string {
	if threads == 0 {
		return ""
	}
	return strconv.Itoa(threads)
}

func parsePort(text string) int {
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}

func modeLabel(mode config.Mode) string {
	if mode == config.ModeRemote {
		return "Remote servers"
	}
	return "This computer"
}

func modeFromLabel(label string) config.Mode {
	if label == "Remote servers" {
		return config.ModeRemote
	}
	return config.ModeLocal
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
