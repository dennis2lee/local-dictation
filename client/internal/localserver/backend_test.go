package localserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

func TestAnUnsetBackendIsTheOneEveryExistingInstallHas(t *testing.T) {
	// The guarantee that lets this setting be added at all: a settings file
	// written before it existed decodes to the empty string, and must go on
	// meaning exactly what it meant before — CPU, the original environment
	// directory, faster-whisper. Anything else silently rebuilds a working
	// install's Python environment on upgrade.
	var unset Backend

	if got := unset.Normalise(); got != BackendCPU {
		t.Errorf("Normalise() = %q, want %q", got, BackendCPU)
	}
	if got := unset.VenvName(); got != "venv" {
		t.Errorf("VenvName() = %q, want the original %q", got, "venv")
	}
	if got := unset.WheelDirName(); got != "wheels" {
		t.Errorf("WheelDirName() = %q, want the original %q", got, "wheels")
	}
	if got := unset.Engine(); got != "whisper" {
		t.Errorf("Engine() = %q, want %q", got, "whisper")
	}
	if got := unset.Device(); got != "cpu" {
		t.Errorf("Device() = %q, want %q", got, "cpu")
	}
	if !unset.Valid() {
		t.Error("an unset backend must be valid; it is what every older settings file holds")
	}
}

func TestEachBackendKeepsItsOwnEnvironmentAndModelFormat(t *testing.T) {
	// Two backends sharing a virtual environment means resolving CTranslate2
	// and OpenVINO's native runtimes together, and two sharing a model
	// directory means one of them reading the other's weights. Both are
	// failures that look like corruption rather than like configuration.
	seen := map[string][]Backend{}
	for _, backend := range Backends() {
		for label, value := range map[string]string{
			"venv":    backend.VenvName(),
			"wheels":  backend.WheelDirName(),
			"engine":  backend.Engine(),
			"weights": backend.Weights(),
		} {
			key := label + "=" + value
			seen[key] = append(seen[key], backend)
		}
	}
	for key, backends := range seen {
		if len(backends) > 1 {
			t.Errorf("%s is shared by %v; every backend needs its own", key, backends)
		}
	}
}

func TestABackendIsOnlyOfferedWhereItsHardwareCanExist(t *testing.T) {
	cases := []struct {
		backend Backend
		goos    string
		want    bool
	}{
		{BackendCPU, "windows", true},
		{BackendCPU, "darwin", true},
		{BackendCPU, "linux", true},
		// OpenVINO's GPU plugin ships for Windows and Linux only.
		{BackendIntelGPU, "windows", true},
		{BackendIntelGPU, "linux", true},
		{BackendIntelGPU, "darwin", false},
		// MLX is Apple Silicon and nothing else.
		{BackendAppleGPU, "darwin", true},
		{BackendAppleGPU, "windows", false},
		{BackendAppleGPU, "linux", false},
	}
	for _, c := range cases {
		if got := c.backend.SupportedOn(c.goos); got != c.want {
			t.Errorf("%s.SupportedOn(%s) = %v, want %v", c.backend, c.goos, got, c.want)
		}
	}
}

func TestAnUnknownBackendIsRejected(t *testing.T) {
	if Backend("nvidia").Valid() {
		t.Error("an unimplemented backend must not validate; it would start a server with --backend nvidia")
	}
}

func TestTheIntelBackendAsksForTheGPUByName(t *testing.T) {
	// Not left to OpenVINO's AUTO device: it falls back to the CPU when the
	// GPU plugin will not load, and the only symptom is that dictation is
	// slow. Naming the device makes that a startup error instead.
	if got := BackendIntelGPU.Device(); got != "GPU" {
		t.Errorf("Device() = %q, want %q", got, "GPU")
	}
	for _, backend := range []Backend{BackendCPU, BackendAppleGPU} {
		if got := backend.Device(); got != "cpu" {
			t.Errorf("%s.Device() = %q, want %q", backend, got, "cpu")
		}
	}
}

func TestEveryBackendInstallsTheInferencePackageItActuallyImports(t *testing.T) {
	// EnsureRuntime probes Modules() and installs PackageSpecs(). A backend
	// whose probe names a module its own specs never install reinstalls on
	// every single start, forever, and never becomes ready.
	imports := map[Backend]string{
		BackendCPU:      "faster-whisper",
		BackendIntelGPU: "openvino-genai",
		BackendAppleGPU: "mlx-whisper",
	}
	for backend, wanted := range imports {
		specs := strings.Join(backend.PackageSpecs(), " ")
		if !strings.Contains(specs, wanted) {
			t.Errorf("%s installs %v, which does not include %s", backend, backend.PackageSpecs(), wanted)
		}
		if len(backend.Modules()) < 5 {
			t.Errorf("%s probes only %v", backend, backend.Modules())
		}
	}
}

func TestTheGeneratedConfigNamesTheBackendsDevice(t *testing.T) {
	// The generated config is the only thing that tells the server which
	// device to compile for; --backend alone would load OpenVINO and run it on
	// the CPU.
	dir := t.TempDir()
	for _, c := range []struct {
		backend Backend
		want    string
	}{
		{BackendIntelGPU, `device: "GPU"`},
		{BackendCPU, `device: "cpu"`},
		{"", `device: "cpu"`},
	} {
		path := filepath.Join(dir, string(c.backend)+"server.yaml")
		options := Options{ModelPath: "/models/whatever", Language: protocol.Korean, Backend: c.backend}
		if err := writeServerConfig(path, options, 8765); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), c.want) {
			t.Errorf("backend %q generated a config without %s:\n%s", c.backend, c.want, raw)
		}
	}
}
