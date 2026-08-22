package ui

import (
	"context"
	"fmt"
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
	levelBar    *widget.ProgressBar
	testButton  *widget.Button
	testStop    chan struct{}
	textAdapter *widget.Label

	// Shortcut and typing
	modifierChecks map[string]*widget.Check
	shortcutKey    *widget.Select
	livePreview    *widget.Check

	// Update
	updateStatus   *widget.Label
	updateButton   *widget.Button
	downloadButton *widget.Button
	// offered is the last check's result, which the download button acts on.
	// Both live on the Fyne goroutine and nothing else reads it.
	offered update.Result
	// Two seams, and only tests move them: where a check goes, and whether the
	// work happens on another goroutine. A test that has to poll a widget for
	// a background goroutine's answer is a test that races with it.
	newSource  func(config.Config) update.Source
	background func(func())

	saveButton *widget.Button
	message    *widget.Label
	body       fyne.CanvasObject
}

func newSettingsTab(app *App) *settingsTab {
	settings := app.Settings()
	tab := &settingsTab{app: app, modifierChecks: map[string]*widget.Check{}}

	tab.message = widget.NewLabel("")
	tab.message.Wrapping = fyne.TextWrapWord

	tab.body = container.NewVBox(
		tab.buildServerSection(settings),
		widget.NewSeparator(),
		tab.buildMicrophoneSection(settings),
		widget.NewSeparator(),
		tab.buildShortcutSection(settings),
		widget.NewSeparator(),
		tab.buildUpdateSection(settings),
		widget.NewSeparator(),
		tab.buildActions(),
		tab.message,
	)
	tab.onModeChanged(modeLabel(settings.Mode))
	return tab
}

func (s *settingsTab) content() fyne.CanvasObject {
	// The inset is inside the scroll, not around it, so the fields clear the
	// scroll bar rather than sliding under it when the tab is long enough to
	// scroll — which this one always is.
	return container.NewVScroll(inset(s.body))
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
			widget.NewFormItem("Python", s.pythonPath),
			widget.NewFormItem("CPU threads", s.cpuThreads),
			widget.NewFormItem("Korean port", s.localKoreanPort),
			widget.NewFormItem("English port", s.localEnglishPort),
		),
		widget.NewLabel("Models are not installed with the app. See docs/model-setup.md."),
		s.localState,
	)

	// Both boxes exist now, so the mode can decide which of them is visible and
	// the handler is safe to attach.
	s.onModeChanged(s.mode.Selected)
	s.mode.OnChanged = s.onModeChanged

	s.koreanLED = newLED("Korean: not checked")
	s.englishLED = newLED("English: not checked")

	return container.NewVBox(
		sectionHeading("Servers"),
		s.mode,
		s.remoteBox,
		s.localBox,
		s.koreanLED.Object(),
		s.englishLED.Object(),
		container.NewHBox(
			primaryButton(widget.NewButtonWithIcon("Test connections", theme.ViewRefreshIcon(), s.onTestConnections)),
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

func (s *settingsTab) onModeChanged(label string) {
	if modeFromLabel(label) == config.ModeLocal {
		s.localBox.Show()
		s.remoteBox.Hide()
	} else {
		s.localBox.Hide()
		s.remoteBox.Show()
	}
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
	s.levelBar = widget.NewProgressBar()
	s.levelBar.Min, s.levelBar.Max = 0, 1
	s.testButton = widget.NewButtonWithIcon("Test microphone", theme.VolumeUpIcon(), s.onTestMicrophone)

	available, reason := s.app.textAdapterAvailability()
	s.textAdapter = widget.NewLabel(reason)
	s.textAdapter.Wrapping = fyne.TextWrapWord
	if available {
		s.textAdapter.Hide()
	}

	s.refreshDevices(settings.Audio.DeviceID)

	return container.NewVBox(
		sectionHeading("Microphone"),
		s.microphone,
		container.NewBorder(nil, nil, nil, s.testButton, s.levelBar),
		widget.NewButtonWithIcon("Refresh device list", theme.ViewRefreshIcon(), func() {
			s.refreshDevices(s.app.Settings().Audio.DeviceID)
		}),
		s.textAdapter,
	)
}

func (s *settingsTab) refreshDevices(selectedID string) {
	devices, err := s.app.capture.Devices()
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
		s.message.SetText(fmt.Sprintf("Could not list microphones: %v", err))
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
		s.levelBar.SetValue(0)
		return
	}

	if !s.app.controller.State().AcceptsSettingsChanges() {
		s.message.SetText("Stop dictation before testing the microphone.")
		return
	}

	device := s.selectedDevice()
	if err := s.app.capture.Start(device.ID, func([]byte) {}); err != nil {
		s.message.SetText(fmt.Sprintf("Could not open %s: %v", device.Name, err))
		return
	}

	stop := make(chan struct{})
	s.testStop = stop
	s.testButton.SetText("Stop test")
	s.message.SetText("Speak normally — the bar should move.")

	go func() {
		ticker := time.NewTicker(60 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				peak, _ := s.app.capture.Meter().Level()
				fyne.Do(func() { s.levelBar.SetValue(peak) })
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
		sectionHeading("Shortcut and typing"),
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

// -- update ----------------------------------------------------------------

func (s *settingsTab) buildUpdateSection(settings config.Config) fyne.CanvasObject {
	s.updateStatus = widget.NewLabel(fmt.Sprintf(
		"Not checked yet. Updates come from %s.", s.updateSource(settings).Describe()))
	s.updateStatus.Wrapping = fyne.TextWrapWord

	s.updateButton = widget.NewButtonWithIcon("Check for updates", theme.SearchIcon(), s.onCheckUpdate)
	// Nothing to download until a check finds something, and a button that is
	// there but does nothing is worse than one that appears when it applies.
	s.downloadButton = primaryButton(
		widget.NewButtonWithIcon("Download and install", theme.DownloadIcon(), s.onDownloadUpdate))
	s.downloadButton.Hide()

	return container.NewVBox(
		sectionHeading("Software update"),
		widget.NewLabel(fmt.Sprintf("Local Dictation %s", s.app.options.Version)),
		s.updateStatus,
		container.NewHBox(s.updateButton, s.downloadButton),
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

func (s *settingsTab) onCheckUpdate() {
	source := s.updateSource(s.app.Settings())
	s.updateStatus.SetText(fmt.Sprintf("Asking %s…", source.Describe()))
	s.updateButton.Disable()
	s.downloadButton.Hide()

	s.away(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		result, err := source.Check(ctx)

		fyne.Do(func() {
			s.updateButton.Enable()
			s.offered = result
			switch {
			case err != nil:
				s.updateStatus.SetText(fmt.Sprintf("Update check failed: %v", err))
			case result.Newer:
				s.updateStatus.SetText(offerText(result))
				s.downloadButton.SetText(fmt.Sprintf("Download and install (%s)", humanSize(result.Artifact.Size)))
				s.downloadButton.Show()
			default:
				s.updateStatus.SetText(fmt.Sprintf(
					"Local Dictation %s is the newest release.", result.Current))
			}
		})
	})
}

// onDownloadUpdate fetches the installer and stops. Running it is the user's
// own act: this application does not hand itself to an installer, and on both
// platforms installing wants a window the user is looking at anyway.
func (s *settingsTab) onDownloadUpdate() {
	offered := s.offered
	s.updateButton.Disable()
	s.downloadButton.Disable()
	s.updateStatus.SetText(fmt.Sprintf("Downloading %s…", installerName(offered.Artifact.URL)))

	s.away(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		saved, err := update.Download(ctx, offered.Artifact, downloadDir(s.app.options.StateDir), nil)

		fyne.Do(func() {
			s.updateButton.Enable()
			if err != nil {
				s.downloadButton.Enable()
				s.updateStatus.SetText(fmt.Sprintf("Download failed: %v", err))
				return
			}
			s.downloadButton.Hide()
			s.installDownloaded(saved, offered.Available)
		})
	})
}

// installDownloaded hands the verified file to the platform installer, then
// closes the application so it can be replaced.
//
// The quit is not optional and not a tidy-up: on both platforms the bundle
// this process is executing from is exactly what the installer overwrites. A
// second process, started first, waits for the installer to finish and opens
// the app again — see update.Install.
func (s *settingsTab) installDownloaded(saved, version string) {
	if err := update.Install(saved, applicationPath()); err != nil {
		// Nothing has been lost: the file is downloaded and verified, and
		// opening it by hand is the same install.
		s.updateStatus.SetText(fmt.Sprintf(
			"Downloaded %s, but the installer would not start: %v\n"+
				"Open %s yourself to finish.", version, err, saved))
		s.downloadButton.Show()
		s.downloadButton.Enable()
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

	if err := s.app.ApplySettings(settings); err != nil {
		s.message.SetText(err.Error())
		dialog.ShowError(err, s.app.window)
		return
	}
	s.message.SetText("Settings saved.")
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
