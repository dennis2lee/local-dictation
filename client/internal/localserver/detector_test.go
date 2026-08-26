package localserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// put writes an empty detector file and returns its path.
func put(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheDetectorBesideTheModelIsFoundWithoutBeingConfigured(t *testing.T) {
	// The whole defect, in one case. fetch-model.sh downloads silero_vad.onnx
	// with every model and Settings › Models installs it into the same place,
	// so it is on disk in almost every install — and the setting that names it
	// is blank in every install where nobody typed it in, which meant the
	// server was configured for the energy detector with the real one sitting
	// next to the weights.
	dir := t.TempDir()
	want := put(t, dir, DetectorName)

	got := ResolveDetector("", filepath.Join(dir, "large-v3-turbo"))
	if got != want {
		t.Errorf("ResolveDetector found %q, want %q", got, want)
	}
}

func TestAConfiguredDetectorWins(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, DetectorName)
	elsewhere := put(t, filepath.Join(dir, "elsewhere"), DetectorName)

	got := ResolveDetector(elsewhere, filepath.Join(dir, "large-v3-turbo"))
	if got != elsewhere {
		t.Errorf("ResolveDetector chose %q over the configured %q", got, elsewhere)
	}
}

func TestAConfiguredPathThatIsNotThereFallsBackToTheOneBesideTheModel(t *testing.T) {
	// A typo, or a models directory that moved. Falling through beats the old
	// behaviour, which was to give up on the detector entirely.
	dir := t.TempDir()
	want := put(t, dir, DetectorName)

	got := ResolveDetector(filepath.Join(dir, "typo", DetectorName), filepath.Join(dir, "large-v3-turbo"))
	if got != want {
		t.Errorf("ResolveDetector returned %q, want the one beside the model %q", got, want)
	}
}

func TestNoDetectorAnywhereIsReportedRatherThanGuessedAt(t *testing.T) {
	dir := t.TempDir()
	if got := ResolveDetector("", filepath.Join(dir, "large-v3-turbo")); got != "" {
		t.Errorf("ResolveDetector invented %q with nothing on disk", got)
	}
	if got := ResolveDetector("", ""); got != "" {
		t.Errorf("ResolveDetector returned %q with nothing configured at all", got)
	}
	// A directory of that name is not a model file.
	if err := os.MkdirAll(filepath.Join(dir, DetectorName), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDetector("", filepath.Join(dir, "large-v3-turbo")); got != "" {
		t.Errorf("ResolveDetector accepted a directory: %q", got)
	}
}

func TestTheGeneratedConfigUsesTheDetectorItFound(t *testing.T) {
	dir := t.TempDir()
	detector := put(t, dir, DetectorName)
	options := Options{ModelPath: filepath.Join(dir, "large-v3-turbo"), Language: protocol.Korean}

	path := filepath.Join(dir, "found.yaml")
	if err := writeServerConfig(path, options, 8765); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `vad: "silero"`) {
		t.Errorf("the server was configured for the energy detector with %s on disk:\n%s", DetectorName, raw)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("silero_model_path: %q", detector)) {
		t.Errorf("generated config does not point at %s:\n%s", detector, raw)
	}
}

func TestTheGeneratedConfigFallsBackOnlyWhenThereIsNoDetector(t *testing.T) {
	dir := t.TempDir()
	options := Options{ModelPath: filepath.Join(dir, "large-v3-turbo"), Language: protocol.Korean}

	path := filepath.Join(dir, "none.yaml")
	if err := writeServerConfig(path, options, 8765); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `vad: "energy"`) {
		t.Errorf("with no detector anywhere the config should say so:\n%s", raw)
	}
	if !strings.Contains(string(raw), "silero_model_path: null") {
		t.Errorf("generated config should carry an explicit null path:\n%s", raw)
	}
}
