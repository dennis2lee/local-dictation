package localserver

import "runtime"

// Backend names the hardware the speech server decodes on.
//
// The client chooses hardware and the server takes an engine name; they are
// deliberately not the same word. "Intel GPU" is what someone picks in
// Settings, "openvino" is what runs, and keeping the translation in one place
// means the UI never has to know the name of a Python package.
type Backend string

const (
	// BackendCPU is faster-whisper on CTranslate2, and works everywhere. It is
	// the default, and the only backend that existed before this setting did —
	// so an empty value means this one, and every settings file written
	// earlier keeps behaving exactly as it did.
	BackendCPU Backend = "cpu"
	// BackendIntelGPU is OpenVINO on an Intel GPU or NPU. Arc 140V on Windows
	// is the first target; nothing here is specific to it.
	BackendIntelGPU Backend = "intel-gpu"
	// BackendAppleGPU is MLX on an Apple Silicon GPU.
	BackendAppleGPU Backend = "apple-gpu"
)

// Backends lists every choice, in the order Settings offers them.
func Backends() []Backend { return []Backend{BackendCPU, BackendIntelGPU, BackendAppleGPU} }

// Normalise maps the empty value onto the default, so that reading a settings
// file written before this field existed does not have to be a special case at
// every call site.
func (b Backend) Normalise() Backend {
	if b == "" {
		return BackendCPU
	}
	return b
}

// Valid reports whether this names a backend at all. It says nothing about
// whether the machine can run it — see SupportedOn.
func (b Backend) Valid() bool {
	switch b.Normalise() {
	case BackendCPU, BackendIntelGPU, BackendAppleGPU:
		return true
	}
	return false
}

// SupportedOn reports whether a backend can run on an operating system at all.
//
// This is about which accelerators exist, not which are installed: OpenVINO's
// GPU plugin ships for Windows and Linux, and MLX only runs on Apple Silicon.
// Offering a choice the machine can never satisfy is worse than not offering
// it, because the failure arrives at the first attempt to dictate.
func (b Backend) SupportedOn(goos string) bool {
	switch b.Normalise() {
	case BackendIntelGPU:
		return goos == "windows" || goos == "linux"
	case BackendAppleGPU:
		return goos == "darwin"
	default:
		return true
	}
}

// Supported reports SupportedOn for the machine this build is running on.
func (b Backend) Supported() bool { return b.SupportedOn(runtime.GOOS) }

// Label is what Settings shows.
func (b Backend) Label() string {
	switch b.Normalise() {
	case BackendIntelGPU:
		return "Intel GPU"
	case BackendAppleGPU:
		return "Apple GPU"
	default:
		return "CPU"
	}
}

// Engine is the server's --backend value.
func (b Backend) Engine() string {
	switch b.Normalise() {
	case BackendIntelGPU:
		return "openvino"
	case BackendAppleGPU:
		return "mlx"
	default:
		return "whisper"
	}
}

// Device is model.device in the generated config.
//
// Named explicitly rather than left to the runtime to choose: OpenVINO will
// run a GPU-targeted model on the CPU when the GPU plugin fails to load, and
// the only symptom a user sees is that dictation is slow.
func (b Backend) Device() string {
	if b.Normalise() == BackendIntelGPU {
		return "GPU"
	}
	return "cpu"
}

// Weights names the file that marks the model conversion this backend reads.
//
// The three conversions live under similar directory names and none of them is
// interchangeable, so this is what the UI and --check quote when the configured
// directory holds the wrong one.
func (b Backend) Weights() string {
	switch b.Normalise() {
	case BackendIntelGPU:
		return "openvino_encoder_model.xml"
	case BackendAppleGPU:
		return "weights.safetensors"
	default:
		return "model.bin"
	}
}

// ModelSuffix is what fetch-model appends for this backend's conversion, so a
// message can name the directory to fetch rather than describing it.
func (b Backend) ModelSuffix() string {
	switch b.Normalise() {
	case BackendIntelGPU:
		return "-openvino-int8"
	case BackendAppleGPU:
		return "-mlx"
	default:
		return ""
	}
}

// VenvName is the environment directory under the state directory.
//
// One per backend, never shared. CTranslate2 and OpenVINO each drag in their
// own native runtime, and resolving both into one environment is how a client
// that worked yesterday fails to import today. The CPU backend keeps the
// original name so existing installs do not rebuild their environment.
func (b Backend) VenvName() string {
	switch b.Normalise() {
	case BackendIntelGPU:
		return "venv-openvino"
	case BackendAppleGPU:
		return "venv-mlx"
	default:
		return "venv"
	}
}

// WheelDirName is the offline wheel directory shipped beside the server, for
// installs on a machine with no package index.
func (b Backend) WheelDirName() string {
	switch b.Normalise() {
	case BackendIntelGPU:
		return "wheels-openvino"
	case BackendAppleGPU:
		return "wheels-mlx"
	default:
		return "wheels"
	}
}

// Modules are what the server imports at startup. The inference package is
// included: without it the server starts but cannot transcribe, and finding
// that out at the first utterance is much worse than finding it out now.
func (b Backend) Modules() []string {
	shared := []string{"fastapi", "uvicorn", "yaml", "numpy"}
	switch b.Normalise() {
	case BackendIntelGPU:
		return append(shared, "openvino", "openvino_genai")
	case BackendAppleGPU:
		return append(shared, "mlx_whisper")
	default:
		return append(shared, "faster_whisper")
	}
}

// PackageSpecs mirrors server/pyproject.toml. Kept as a list rather than a
// requirements file so the installers have one fewer payload to keep in step.
func (b Backend) PackageSpecs() []string {
	shared := []string{
		"fastapi>=0.115",
		"uvicorn[standard]>=0.30",
		"pyyaml>=6.0",
		"numpy>=1.26",
		"onnxruntime>=1.18",
	}
	switch b.Normalise() {
	case BackendIntelGPU:
		return append(shared, "openvino-genai>=2025.0")
	case BackendAppleGPU:
		return append(shared, "mlx-whisper>=0.4")
	default:
		return append(shared, "faster-whisper>=1.0.3")
	}
}
