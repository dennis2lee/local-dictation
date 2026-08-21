package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/update"
)

func appAtVersion(settings config.Config, version string) *App {
	app := halfBuiltApp(settings)
	app.options = Options{Version: version, StateDir: "/state"}
	return app
}

// The bug: without an internal distribution server the button was disabled and
// the only thing this section could say was that updates were not configured —
// which is every install made from the published packages.
func TestUpdatesCanBeCheckedWithoutAnInternalServer(t *testing.T) {
	settings := testSettings()
	settings.Update.ManifestURL = ""
	tab := &settingsTab{app: appAtVersion(settings, "0.1.10")}

	tab.buildUpdateSection(settings)

	if tab.updateButton.Disabled() {
		t.Error("the check button is disabled on a default install")
	}
	if !strings.Contains(tab.updateStatus.Text, "github.com/"+update.DefaultRepo) {
		t.Errorf("status reads %q, want it to name where a check goes", tab.updateStatus.Text)
	}
	if _, ok := tab.updateSource(settings).(update.GitHub); !ok {
		t.Errorf("a default install checks %T", tab.updateSource(settings))
	}
}

// A managed deployment sets a manifest URL, and that one is signed. Falling
// back to github.com when it is configured would quietly weaken it.
func TestAConfiguredInternalServerIsStillWhatGetsChecked(t *testing.T) {
	settings := testSettings()
	settings.Update.ManifestURL = "https://dist.internal/local-dictation.json"
	settings.Update.PublicKey = "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyc28="
	tab := &settingsTab{app: appAtVersion(settings, "0.1.10")}

	tab.buildUpdateSection(settings)

	if _, ok := tab.updateSource(settings).(update.Checker); !ok {
		t.Fatalf("a configured manifest URL checks %T", tab.updateSource(settings))
	}
	if !strings.Contains(tab.updateStatus.Text, "dist.internal") {
		t.Errorf("status reads %q, want the internal server named", tab.updateStatus.Text)
	}
}

func TestAForkCanPointChecksAtItsOwnReleases(t *testing.T) {
	settings := testSettings()
	settings.Update.GitHubRepo = "someone/their-fork"
	tab := &settingsTab{app: appAtVersion(settings, "0.1.10")}

	tab.buildUpdateSection(settings)

	if !strings.Contains(tab.updateStatus.Text, "someone/their-fork") {
		t.Errorf("status reads %q, want the configured repository", tab.updateStatus.Text)
	}
}

// There is nothing to download before a check has found something.
func TestTheDownloadButtonWaitsForACheck(t *testing.T) {
	settings := testSettings()
	tab := &settingsTab{app: appAtVersion(settings, "0.1.10")}

	tab.buildUpdateSection(settings)

	if tab.downloadButton.Visible() {
		t.Error("a download button is offered before anything has been found")
	}
}

func TestDownloadsLandWhereSomeoneWillLookForThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows

	// No Downloads folder: the app's own directory rather than dropping the
	// file somewhere the user has no reason to visit.
	if got, want := downloadDir("/state"), filepath.Join("/state", "updates"); got != want {
		t.Errorf("without a Downloads folder the installer goes to %q, want %q", got, want)
	}

	downloads := filepath.Join(home, "Downloads")
	if err := os.Mkdir(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := downloadDir("/state"); got != downloads {
		t.Errorf("installer goes to %q, want %q", got, downloads)
	}
}

func TestSizesAreShownInUnitsAPersonReads(t *testing.T) {
	for _, want := range []struct {
		bytes int64
		text  string
	}{
		{0, "unknown size"},
		{-1, "unknown size"},
		{4096, "4 KB"},
		{25_341_952, "24.2 MB"},
	} {
		if got := humanSize(want.bytes); got != want.text {
			t.Errorf("humanSize(%d) = %q, want %q", want.bytes, got, want.text)
		}
	}
}

func TestAnOfferNamesTheVersionAndWhereToReadAboutIt(t *testing.T) {
	text := offerText(update.Result{
		Available: "0.2.0",
		Page:      "https://github.com/dennis2lee/local-dictation/releases/tag/v0.2.0",
	})
	if !strings.Contains(text, "0.2.0") || !strings.Contains(text, "releases/tag/v0.2.0") {
		t.Errorf("offer reads %q", text)
	}
}

func TestAnInstallerIsNamedByItsFile(t *testing.T) {
	got := installerName("https://github.com/o/r/releases/download/v0.2.0/LocalDictation-0.2.0.pkg")
	if got != "LocalDictation-0.2.0.pkg" {
		t.Errorf("installerName = %q", got)
	}
	if got := installerName(""); got == "" {
		t.Error("an empty URL produced an empty name, which reads as a truncated sentence")
	}
}

// stubSource stands in for github.com so no test reaches the network.
type stubSource struct {
	result update.Result
	err    error
	asked  int
}

func (s *stubSource) Check(context.Context) (update.Result, error) {
	s.asked++
	return s.result, s.err
}

func (s *stubSource) Describe() string { return "the test source" }

// checkedTab wires a tab to a stub source and runs the "background" work
// inline, so a test never has to poll a widget another goroutine is writing.
func checkedTab(t *testing.T, source update.Source) *settingsTab {
	t.Helper()
	settings := testSettings()
	tab := &settingsTab{
		app:        appAtVersion(settings, "0.1.10"),
		newSource:  func(config.Config) update.Source { return source },
		background: func(work func()) { work() },
	}
	tab.buildUpdateSection(settings)
	return tab
}

func TestANewerReleaseIsOfferedAsADownload(t *testing.T) {
	tab := checkedTab(t, &stubSource{result: update.Result{
		Current:   "0.1.10",
		Available: "0.2.0",
		Newer:     true,
		Page:      "https://github.com/dennis2lee/local-dictation/releases/tag/v0.2.0",
		Artifact: update.Artifact{
			URL:    "https://github.com/o/r/releases/download/v0.2.0/LocalDictation-0.2.0.pkg",
			SHA256: "abc",
			Size:   25_341_952,
		},
	}})

	tab.onCheckUpdate()

	if !tab.downloadButton.Visible() {
		t.Fatal("a newer release was found and no download was offered")
	}
	if !strings.Contains(tab.downloadButton.Text, "24.2 MB") {
		t.Errorf("download button reads %q, want the size in it", tab.downloadButton.Text)
	}
	if !strings.Contains(tab.updateStatus.Text, "0.2.0") {
		t.Errorf("status reads %q, want the new version named", tab.updateStatus.Text)
	}
	if tab.updateButton.Disabled() {
		t.Error("the check button was left disabled after the check finished")
	}
}

func TestAnUpToDateClientIsToldSo(t *testing.T) {
	tab := checkedTab(t, &stubSource{result: update.Result{Current: "0.1.10"}})

	tab.onCheckUpdate()

	if tab.downloadButton.Visible() {
		t.Error("a download was offered for a version already installed")
	}
	if !strings.Contains(tab.updateStatus.Text, "newest") {
		t.Errorf("status reads %q", tab.updateStatus.Text)
	}
}

// A check that cannot reach anywhere must leave the button pressable — the
// usual cause is a network that will be back in a minute.
func TestAFailedCheckSaysWhyAndCanBeRetried(t *testing.T) {
	tab := checkedTab(t, &stubSource{err: update.ErrRateLimited})

	tab.onCheckUpdate()

	if tab.updateButton.Disabled() {
		t.Error("a failed check left the button disabled, so it cannot be retried")
	}
	if !strings.Contains(tab.updateStatus.Text, "rate-limiting") {
		t.Errorf("status reads %q, want the reason", tab.updateStatus.Text)
	}
}

// check_on_start was a settings field that nothing read.
func TestCheckOnStartActuallyChecks(t *testing.T) {
	source := &stubSource{result: update.Result{Current: "0.1.10"}}

	settings := testSettings()
	settings.Update.CheckOnStart = false
	app := appAtVersion(settings, "0.1.10")
	app.settingsTab = &settingsTab{
		app:        app,
		newSource:  func(config.Config) update.Source { return source },
		background: func(work func()) { work() },
	}
	app.settingsTab.buildUpdateSection(settings)

	app.checkForUpdatesOnStart()
	if source.asked != 0 {
		t.Fatalf("a check ran %d times with check_on_start off", source.asked)
	}

	settings.Update.CheckOnStart = true
	app.settings = settings
	app.checkForUpdatesOnStart()
	if source.asked != 1 {
		t.Fatalf("check_on_start asked %d times, want 1", source.asked)
	}
}
