package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/localserver"
)

func TestTheBackendChoiceSurvivesASave(t *testing.T) {
	// The round trip is the whole feature: a selector that shows the right
	// thing and writes nothing back looks like it worked until the next start.
	settings := testSettings()
	settings.Local.Backend = localserver.BackendCPU
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	chosen := gpuBackendHere(t)
	tab.backend.SetSelected(chosen.Label())
	if saved := tab.collect(settings); saved.Local.Backend != chosen {
		t.Errorf("saved backend = %q, want %q", saved.Local.Backend, chosen)
	}

	tab.backend.SetSelected(localserver.BackendCPU.Label())
	if saved := tab.collect(settings); saved.Local.Backend != localserver.BackendCPU {
		t.Errorf("saved backend = %q, want %q", saved.Local.Backend, localserver.BackendCPU)
	}
}

func TestSavingAGPUBackendLeavesEverythingElseAlone(t *testing.T) {
	// Backend was threaded through ManagerSettings, which Manager.Update
	// compares field by field to decide whether to restart a loaded model.
	// A save that quietly changed something else would restart both servers.
	settings := testSettings()
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	saved := tab.collect(settings)

	if saved.Local.ModelPath != settings.Local.ModelPath {
		t.Errorf("model path changed to %q", saved.Local.ModelPath)
	}
	if saved.Mode != settings.Mode {
		t.Errorf("mode changed to %q", saved.Mode)
	}
}

func TestAnUnsetBackendShowsAsCPURatherThanBlank(t *testing.T) {
	// A settings file from before this field existed holds "". An unselected
	// radio would read as "no backend chosen" for a choice that has in fact
	// always been made — and would then save the empty string back.
	settings := testSettings()
	settings.Local.Backend = ""
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	if got := tab.backend.Selected; got != localserver.BackendCPU.Label() {
		t.Errorf("an unset backend shows as %q, want %q", got, localserver.BackendCPU.Label())
	}
	if saved := tab.collect(settings); saved.Local.Backend != localserver.BackendCPU {
		t.Errorf("an untouched form saved %q, want %q", saved.Local.Backend, localserver.BackendCPU)
	}
}

func TestOnlyBackendsThisMachineCanRunAreOffered(t *testing.T) {
	offered := backendLabels()
	if len(offered) == 0 {
		t.Fatal("no backend offered at all")
	}
	for _, label := range offered {
		if !backendFor(label).SupportedOn(runtime.GOOS) {
			t.Errorf("%q is offered on %s, where its hardware cannot exist", label, runtime.GOOS)
		}
	}
	if offered[0] != localserver.BackendCPU.Label() {
		t.Errorf("first choice is %q; CPU should lead, it is the one that always works", offered[0])
	}
}

func TestTheNoteNamesTheModelFileTheChosenBackendReads(t *testing.T) {
	// The mistake this heads off is pointing every backend at one directory.
	// The three conversions are not interchangeable and their directory names
	// differ only by a suffix.
	settings := testSettings()
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	for _, backend := range localserver.Backends() {
		if !backend.Supported() {
			continue
		}
		tab.onBackendChanged(backend.Label())
		if note := tab.backendNote.Text; !strings.Contains(note, backend.Weights()) {
			t.Errorf("the note for %s is %q, which does not name %s",
				backend.Label(), note, backend.Weights())
		}
	}
}

func TestTheBackendIsLockedWhileDictating(t *testing.T) {
	// Every other setting is. Switching the decoder out from under a running
	// server would leave the two disagreeing about which model is loaded.
	settings := testSettings()
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	tab.setEditable(false)
	if !tab.backend.Disabled() {
		t.Error("the backend selector stayed editable while dictation was running")
	}
	tab.setEditable(true)
	if tab.backend.Disabled() {
		t.Error("the backend selector stayed locked after dictation stopped")
	}
}

// gpuBackendHere is whichever accelerator this machine could use, so the test
// exercises a real switch rather than selecting the CPU twice.
func gpuBackendHere(t *testing.T) localserver.Backend {
	t.Helper()
	for _, backend := range localserver.Backends() {
		if backend != localserver.BackendCPU && backend.Supported() {
			return backend
		}
	}
	t.Skip("no accelerator backend is supported on this operating system")
	return localserver.BackendCPU
}

func TestChoosingAGPUWithTheCPUModelSaysSoBeforeDictating(t *testing.T) {
	// What 0.1.24 did on a Windows laptop: Intel GPU was chosen, the model
	// directory still held the CTranslate2 conversion, Save reported success,
	// and the first press of the shortcut produced a wall of text from a
	// Python process that had already exited. The directory and the backend
	// are both on screen when the choice is made; this is where it belongs.
	models := t.TempDir()
	cpuModel := filepath.Join(models, "large-v3-turbo")
	if err := os.MkdirAll(cpuModel, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cpuModel, "model.bin"), []byte("ct2"), 0o600); err != nil {
		t.Fatal(err)
	}

	settings := testSettings()
	settings.Local.ModelPath = cpuModel
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	// The CPU backend is happy with it.
	tab.onBackendChanged(localserver.BackendCPU.Label())
	if note := tab.backendNote.Text; strings.Contains(note, "cannot read it") {
		t.Errorf("the CPU backend complained about its own model: %q", note)
	}

	// Every accelerator is not, and says which file is missing.
	for _, backend := range []localserver.Backend{localserver.BackendIntelGPU, localserver.BackendAppleGPU} {
		note := modelProblem(backend, cpuModel)
		if note == "" {
			t.Fatalf("%s accepted a directory holding only model.bin", backend.Label())
		}
		if !strings.Contains(note, backend.Weights()) {
			t.Errorf("%s said %q, which does not name %s", backend.Label(), note, backend.Weights())
		}
	}
}

func TestTheRightConversionBesideTheWrongOneIsNamed(t *testing.T) {
	// fetch-model puts them next to each other under names differing by a
	// suffix. Someone who has downloaded both is one directory away, and
	// naming it saves them going to look up what it is called.
	models := t.TempDir()
	cpuModel := filepath.Join(models, "large-v3-turbo")
	gpuModel := filepath.Join(models, "large-v3-turbo"+localserver.BackendIntelGPU.ModelSuffix())
	for dir, weights := range map[string]string{
		cpuModel: localserver.BackendCPU.Weights(),
		gpuModel: localserver.BackendIntelGPU.Weights(),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, weights), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	problem := modelProblem(localserver.BackendIntelGPU, cpuModel)

	if !strings.Contains(problem, filepath.Base(gpuModel)) {
		t.Errorf("the sibling export was not offered: %q", problem)
	}
}

func TestAModelDirectoryThatIsNotThereYetIsNotAComplaint(t *testing.T) {
	// Configuring before downloading is a normal order to work in. Complaining
	// about an absent path would make the note permanent noise during setup,
	// and both --check and the server say so plainly when it matters.
	if problem := modelProblem(localserver.BackendIntelGPU, filepath.Join(t.TempDir(), "not-yet")); problem != "" {
		t.Errorf("an absent directory was reported as a problem: %q", problem)
	}
	if problem := modelProblem(localserver.BackendIntelGPU, ""); problem != "" {
		t.Errorf("an empty path was reported as a problem: %q", problem)
	}
}
