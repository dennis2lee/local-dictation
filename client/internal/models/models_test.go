package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/localserver"
)

// -- staying in step with the scripts ---------------------------------------

func TestTheCatalogueMatchesBothFetchScripts(t *testing.T) {
	// 0.1.24 shipped the OpenVINO conversions in fetch-model.sh and not in
	// fetch-model.ps1 — on Windows, the one platform the Intel GPU backend
	// targets, there was no supported way to get its model. Nothing caught it
	// because nothing compared the two.
	//
	// Now there are three places a model name and its repository are written
	// down, and the only defence against them drifting is reading the other
	// two from here.
	scripts := []struct {
		name string
		goos string
		body string
	}{
		// Each script serves the platforms it ships to, and is only held to
		// the models those platforms can run: MLX is deliberately absent from
		// the PowerShell one because Apple Silicon is not a Windows machine.
		{"fetch-model.sh", "darwin", readScript(t, "fetch-model.sh")},
		{"fetch-model.sh", "linux", readScript(t, "fetch-model.sh")},
		{"fetch-model.ps1", "windows", readScript(t, "fetch-model.ps1")},
	}

	for _, script := range scripts {
		for _, model := range Catalogue() {
			if model.Repo == "" {
				// The detector comes from its own project, by URL, and every
				// platform needs it.
				if !strings.Contains(script.body, SileroURL) {
					t.Errorf("%s does not carry the detector URL", script.name)
				}
				continue
			}
			if !model.Backend.SupportedOn(script.goos) {
				continue
			}
			if !strings.Contains(script.body, model.Repo) {
				t.Errorf("%s (%s) does not know %s (%s)",
					script.name, script.goos, model.Name, model.Repo)
			}
			if !strings.Contains(script.body, model.Name) {
				t.Errorf("%s (%s) cannot be asked for %q", script.name, script.goos, model.Name)
			}
		}
	}
}

func TestEveryRepositoryTheScriptsKnowIsInTheCatalogue(t *testing.T) {
	// The other direction: a model added to the scripts and not here would be
	// downloadable from a terminal and invisible in the app.
	shell := readScript(t, "fetch-model.sh")
	known := map[string]bool{}
	for _, model := range Catalogue() {
		known[model.Repo] = true
	}

	// REPO_SOMETHING="owner/name"
	pattern := regexp.MustCompile(`(?m)^REPO_[A-Z0-9_]+="([^"]+)"`)
	for _, match := range pattern.FindAllStringSubmatch(shell, -1) {
		if !known[match[1]] {
			t.Errorf("fetch-model.sh offers %s, which the Models tab cannot install", match[1])
		}
	}
}

func readScript(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "server", "scripts", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// -- the catalogue itself ---------------------------------------------------

func TestEveryBackendHasAnAccurateModelToOffer(t *testing.T) {
	// A backend with nothing to install is a backend someone can select and
	// then be stuck on.
	for _, backend := range localserver.Backends() {
		var accurate int
		for _, model := range For(backend) {
			if model.Role == Accurate {
				accurate++
			}
		}
		if accurate == 0 {
			t.Errorf("%s has no accurate model in the catalogue", backend.Label())
		}
	}
}

func TestTheDetectorIsOfferedToEveryBackend(t *testing.T) {
	// It is not a Whisper conversion — every backend uses the same file, and
	// leaving it out of a list would read as "not needed here".
	for _, backend := range localserver.Backends() {
		found := false
		for _, model := range For(backend) {
			found = found || model.Role == Detector
		}
		if !found {
			t.Errorf("%s is not offered the voice activity detector", backend.Label())
		}
	}
}

func TestAModelIsOnlyOfferedToTheBackendThatCanReadIt(t *testing.T) {
	for _, model := range For(localserver.BackendIntelGPU) {
		if model.Role == Detector {
			continue
		}
		if got := model.Weights(); got != "openvino_encoder_model.xml" {
			t.Errorf("%s is offered to Intel GPU but reads %s", model.Name, got)
		}
	}
}

// -- scanning ---------------------------------------------------------------

func TestAHalfDownloadedModelIsNotInstalled(t *testing.T) {
	// The failure this prevents: an interrupted download leaves a directory
	// holding a tokenizer and no weights. Treating the directory as proof
	// would report it installed and fail at load, which is a much longer walk
	// back to "press Get again".
	dir := t.TempDir()
	model, _ := Find("large-v3-turbo")
	partial := filepath.Join(dir, model.Name)
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "tokenizer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if Stat(model, dir).Installed {
		t.Error("a directory with no weights in it was reported as installed")
	}

	if err := os.WriteFile(filepath.Join(partial, "model.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := Stat(model, dir)
	if !state.Installed {
		t.Error("a directory holding model.bin was not reported as installed")
	}
	if state.Bytes == 0 {
		t.Error("an installed model reported no size")
	}
}

func TestScanCoversTheWholeCatalogue(t *testing.T) {
	if got, want := len(Scan(t.TempDir())), len(Catalogue()); got != want {
		t.Errorf("scanned %d models, catalogue has %d", got, want)
	}
}

// -- installing -------------------------------------------------------------

func fakeHub(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/models/") {
			type sibling struct {
				Name string `json:"rfilename"`
			}
			var listing struct {
				Siblings []sibling `json:"siblings"`
			}
			for name := range files {
				listing.Siblings = append(listing.Siblings, sibling{Name: name})
			}
			// Repository furniture, which must not be downloaded.
			listing.Siblings = append(listing.Siblings,
				sibling{Name: ".gitattributes"}, sibling{Name: "README.md"})
			_ = json.NewEncoder(w).Encode(listing)
			return
		}
		for name, body := range files {
			if strings.HasSuffix(r.URL.Path, "/resolve/main/"+name) {
				fmt.Fprint(w, body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("HF_ENDPOINT", server.URL)
	return server
}

func TestInstallWritesTheFilesAndTheirChecksums(t *testing.T) {
	fakeHub(t, map[string]string{"model.bin": "weights", "tokenizer.json": "{}"})
	dir := t.TempDir()
	model, _ := Find("large-v3-turbo")

	var seen []string
	if err := Install(context.Background(), model, dir, func(p Progress) {
		seen = append(seen, p.File)
	}); err != nil {
		t.Fatal(err)
	}

	if !Stat(model, dir).Installed {
		t.Fatal("the model did not end up installed")
	}
	sums, err := os.ReadFile(filepath.Join(dir, model.Name, "SHA256SUMS"))
	if err != nil {
		t.Fatalf("no SHA256SUMS written: %v", err)
	}
	// sha256("weights")
	if !strings.Contains(string(sums), "model.bin") {
		t.Errorf("SHA256SUMS does not list model.bin:\n%s", sums)
	}
	if len(seen) == 0 {
		t.Error("no progress was reported")
	}
}

func TestRepositoryFurnitureIsNotDownloaded(t *testing.T) {
	fakeHub(t, map[string]string{"model.bin": "weights"})
	dir := t.TempDir()
	model, _ := Find("large-v3-turbo")

	if err := Install(context.Background(), model, dir, nil); err != nil {
		t.Fatal(err)
	}

	for _, unwanted := range []string{".gitattributes", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, model.Name, unwanted)); err == nil {
			t.Errorf("%s was downloaded", unwanted)
		}
	}
}

func TestAFailedInstallLeavesNothingThatLooksInstalled(t *testing.T) {
	// The whole reason files are staged: a half-written directory that reports
	// itself installed sends someone to debug a load failure instead of
	// pressing Get again.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/models/") {
			fmt.Fprint(w, `{"siblings":[{"rfilename":"model.bin"},{"rfilename":"tokenizer.json"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "tokenizer.json") {
			http.Error(w, "gone", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "weights")
	}))
	defer server.Close()
	t.Setenv("HF_ENDPOINT", server.URL)

	dir := t.TempDir()
	model, _ := Find("large-v3-turbo")

	if err := Install(context.Background(), model, dir, nil); err == nil {
		t.Fatal("a failed download reported success")
	}
	if Stat(model, dir).Installed {
		t.Error("a failed download left something that reports itself installed")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			t.Errorf("a failed download left %q behind", entry.Name())
		}
	}
}

func TestAnExistingInstallSurvivesAFailedReinstall(t *testing.T) {
	dir := t.TempDir()
	model, _ := Find("large-v3-turbo")
	installed := filepath.Join(dir, model.Name)
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "model.bin"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("HF_ENDPOINT", server.URL)

	if err := Install(context.Background(), model, dir, nil); err == nil {
		t.Fatal("expected a failure")
	}

	body, err := os.ReadFile(filepath.Join(installed, "model.bin"))
	if err != nil || string(body) != "original" {
		t.Errorf("the working install was damaged by a failed reinstall: %q, %v", body, err)
	}
}

func TestAPathFromTheRepositoryCannotEscapeTheDirectory(t *testing.T) {
	// The file list comes from a remote server. It is not a trusted source of
	// paths, and joining one blindly is how ../ ends up written outside.
	for _, hostile := range []string{"../escape", "nested/file", `..\windows`, "/absolute"} {
		if !skipped(hostile) {
			t.Errorf("%q was accepted as a file to download", hostile)
		}
	}
	for _, ordinary := range []string{"model.bin", "config.json", "openvino_encoder_model.xml"} {
		if skipped(ordinary) {
			t.Errorf("%q was skipped", ordinary)
		}
	}
}

func TestTheDetectorInstallsAsASingleFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "onnx bytes")
	}))
	defer server.Close()

	dir := t.TempDir()
	model, _ := Find("silero_vad.onnx")
	model.URL = server.URL + "/silero_vad.onnx"

	if err := Install(context.Background(), model, dir, nil); err != nil {
		t.Fatal(err)
	}
	if !Stat(model, dir).Installed {
		t.Error("the detector did not end up installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "silero_vad.onnx.partial")); err == nil {
		t.Error("a .partial file was left behind")
	}
}
