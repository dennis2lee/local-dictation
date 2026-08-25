package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/localserver"
	"github.com/dennis2lee/local-dictation/client/internal/models"
)

// installed writes a directory that Scan will accept for this backend.
func installed(t *testing.T, dir, name string, backend localserver.Backend) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, backend.Weights()), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func modelsTab(t *testing.T, dir string) *settingsTab {
	t.Helper()
	settings := testSettings()
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo")
	return newTestSettingsTab(halfBuiltApp(settings), settings)
}

func TestTheListSaysWhatIsInstalledAndWhatIsNot(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "large-v3-turbo", localserver.BackendCPU)
	tab := modelsTab(t, dir)

	states := map[string]bool{}
	for _, state := range models.Scan(tab.modelsDir()) {
		states[state.Model.Name] = state.Installed
	}

	if !states["large-v3-turbo"] {
		t.Error("a model that is on disk was not reported as installed")
	}
	if states["base"] {
		t.Error("a model that is not on disk was reported as installed")
	}
}

func TestTheModelsYouNeedAreVisibleWithoutScrolling(t *testing.T) {
	// The Models group is a list and scrolls, so the one thing it owes is that
	// the models the chosen backend actually needs are not the ones behind the
	// scroll. Someone opens this tab because dictation will not start.
	dir := t.TempDir()
	settings := testSettings()
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo")
	app := halfBuiltApp(settings)
	test.ApplyTheme(t, planTheme{})
	tab := newTestSettingsTab(app, settings)

	outer := container.NewAppTabs(
		container.NewTabItem("Main", widget.NewLabel("")),
		container.NewTabItem("Settings", tab.content()),
	)
	outer.SelectIndex(1)
	window := test.NewWindow(outer)
	defer window.Close()
	window.Resize(fyne.NewSize(windowWidth, windowHeight))

	index := groupIndex(t, tab, "Models")
	tab.groups.SelectIndex(index)
	scroll := tab.groups.Items[index].Content.(*container.Scroll)

	// The chosen backend's models are added first, under their own heading, so
	// the last of them is what has to land inside the visible height.
	wanted := len(models.For(backendFor(tab.backend.Selected)))
	if wanted == 0 {
		t.Fatal("the selected backend has no models at all")
	}
	// heading + one row per model
	rows := tab.modelsBox.Objects[:wanted+1]
	driver := fyne.CurrentApp().Driver()
	top := driver.AbsolutePositionForObject(scroll).Y
	last := rows[len(rows)-1]
	bottom := driver.AbsolutePositionForObject(last).Y + last.Size().Height

	if bottom > top+scroll.Size().Height {
		t.Errorf("the last model this backend needs ends at %.0f, below the %.0f visible",
			bottom-top, scroll.Size().Height)
	}
}

func groupIndex(t *testing.T, tab *settingsTab, name string) int {
	t.Helper()
	for index, item := range tab.groups.Items {
		if item.Text == name {
			return index
		}
	}
	t.Fatalf("no %q group", name)
	return -1
}

func TestTheListFollowsTheChosenBackend(t *testing.T) {
	// "For Intel GPU" is the first heading, and the models under it are the
	// ones that backend can read. Switching the selector has to re-sort it, or
	// the tab answers a question nobody asked.
	dir := t.TempDir()
	tab := modelsTab(t, dir)

	for _, backend := range localserver.Backends() {
		if !backend.Supported() {
			continue
		}
		tab.backend.SetSelected(backend.Label())

		heading, ok := tab.modelsBox.Objects[0].(*widget.Label)
		if !ok {
			t.Fatalf("first object is %T, not a heading", tab.modelsBox.Objects[0])
		}
		if !strings.Contains(heading.Text, backend.Label()) {
			t.Errorf("with %s selected the list is headed %q", backend.Label(), heading.Text)
		}
	}
}

func TestDownloadingForTheChosenBackendPointsModelDirectoryAtIt(t *testing.T) {
	// The state that made this tab necessary: the model is downloaded, and the
	// Model directory still names the other conversion, so the next press of
	// the shortcut fails exactly as before.
	dir := t.TempDir()
	settings := testSettings()
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo") // the CPU one
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	gpu := gpuBackendHere(t)
	tab.backend.SetSelected(gpu.Label())

	var wanted models.Model
	for _, model := range models.For(gpu) {
		if model.Role == models.Accurate {
			wanted = model
			break
		}
	}
	installed(t, dir, wanted.Name, gpu)
	tab.finishInstall(wanted, dir, nil)

	if got := tab.modelPath.Text; got != filepath.Join(dir, wanted.Name) {
		t.Errorf("Model directory is %q, want the model just installed", got)
	}
	if !strings.Contains(tab.modelsStatus.Text, "Save") {
		t.Errorf("nothing said the change still needs saving: %q", tab.modelsStatus.Text)
	}
	// And it is not saved behind their back.
	if tab.app.Settings().Local.ModelPath == tab.modelPath.Text {
		t.Error("the setting was applied without Save being pressed")
	}
}

func TestADownloadForAnotherBackendLeavesTheSettingAlone(t *testing.T) {
	dir := t.TempDir()
	settings := testSettings()
	installed(t, dir, "large-v3-turbo", localserver.BackendCPU)
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo")
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)
	tab.backend.SetSelected(localserver.BackendCPU.Label())

	before := tab.modelPath.Text
	draft, _ := models.Find("base")
	tab.finishInstall(draft, dir, nil)

	if tab.modelPath.Text != before {
		t.Errorf("downloading the draft model moved Model directory to %q", tab.modelPath.Text)
	}
}

func TestAFailedDownloadSaysSoAndKeepsTheTabUsable(t *testing.T) {
	dir := t.TempDir()
	tab := modelsTab(t, dir)
	model, _ := models.Find("large-v3-turbo")

	tab.installing = true
	tab.finishInstall(model, dir, errors.New("the network went away"))

	if tab.installing {
		t.Error("the tab stayed locked after a failed download")
	}
	if !strings.Contains(tab.modelsStatus.Text, "network went away") {
		t.Errorf("the failure was not reported: %q", tab.modelsStatus.Text)
	}
	if !tab.modelsProgress.Hidden {
		t.Error("the progress bar was left on screen after a failure")
	}
}

func TestTheTabSaysWhereModelsGo(t *testing.T) {
	// Downloads land beside whatever Model directory names, not where a
	// script's default would have put them — which on Windows is a different
	// directory entirely. Saying which one removes the guess.
	dir := t.TempDir()
	tab := modelsTab(t, dir)

	if !strings.Contains(tab.modelsWhere.Text, dir) {
		t.Errorf("the tab says %q, which does not name %s", tab.modelsWhere.Text, dir)
	}
}

func TestInstallingIsNotStartedTwice(t *testing.T) {
	dir := t.TempDir()
	tab := modelsTab(t, dir)
	calls := 0
	tab.installModel = func(context.Context, models.Model, string, func(models.Progress)) error {
		calls++
		return nil
	}
	tab.installing = true

	model, _ := models.Find("base")
	tab.onInstallModel(model)

	if calls != 0 {
		t.Errorf("a second download started while one was running (%d calls)", calls)
	}
}

func TestOnlyAnEmptyBackendIsMarkedUrgent(t *testing.T) {
	// large-v3-turbo and large-v3 both serve the CPU. With one installed the
	// other is an alternative, not a missing prerequisite, and colouring it as
	// a problem says four things are wrong when nothing is.
	dir := t.TempDir()
	settings := testSettings()
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo")
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)
	tab.backend.SetSelected(localserver.BackendCPU.Label())

	// Nothing installed: the accurate models are the problem.
	if urgent := urgentNames(tab); len(urgent) == 0 {
		t.Error("with no model installed, nothing was marked as missing")
	}

	// One installed: nothing is urgent any more.
	installed(t, dir, "large-v3-turbo", localserver.BackendCPU)
	tab.refreshModels()

	if urgent := urgentNames(tab); len(urgent) != 0 {
		t.Errorf("with large-v3-turbo installed, %v is still flagged as missing", urgent)
	}
}

// urgentNames is the models the list is showing in red.
func urgentNames(tab *settingsTab) []string {
	var names []string
	for _, object := range tab.modelsBox.Objects {
		row, ok := object.(*fyne.Container)
		if !ok {
			continue
		}
		for _, child := range row.Objects {
			if label, ok := child.(*widget.Label); ok && label.Importance == widget.DangerImportance {
				names = append(names, label.Text)
			}
		}
	}
	return names
}

func TestTheDetectorIsNeverMarkedUrgent(t *testing.T) {
	// It is optional — without it the server falls back to an energy
	// threshold — so it must not read as the reason dictation will not start.
	dir := t.TempDir()
	settings := testSettings()
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo")
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	for _, name := range urgentNames(tab) {
		if name == "silero_vad.onnx" {
			t.Error("the voice activity detector was flagged as a missing prerequisite")
		}
	}
}

func TestTheListReSortsEvenWhenTheModelDirectoryIsWrong(t *testing.T) {
	// The regression: choosing a GPU backend while Model directory still held
	// the CPU conversion took the warning path in onBackendChanged, which
	// returned before refreshing the list. So the Models tab went on offering
	// the old backend's models in exactly the situation someone opens it to
	// fix — the model is wrong and they need to see the right one.
	dir := t.TempDir()
	installed(t, dir, "large-v3-turbo", localserver.BackendCPU)
	settings := testSettings()
	settings.Local.ModelPath = filepath.Join(dir, "large-v3-turbo")
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	gpu := gpuBackendHere(t)
	tab.backend.SetSelected(gpu.Label())

	heading, ok := tab.modelsBox.Objects[0].(*widget.Label)
	if !ok {
		t.Fatalf("first object is %T", tab.modelsBox.Objects[0])
	}
	if !strings.Contains(heading.Text, gpu.Label()) {
		t.Errorf("the list is headed %q after switching to %s", heading.Text, gpu.Label())
	}
	// And the warning is still shown — the fix must not have swallowed it.
	if !strings.Contains(tab.backendNote.Text, gpu.Weights()) {
		t.Errorf("the mismatch warning was lost: %q", tab.backendNote.Text)
	}
}
