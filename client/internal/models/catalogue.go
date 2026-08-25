// Package models is the catalogue of speech models and the code that installs
// them.
//
// The same ground the fetch-model scripts cover, in Go, so that the client can
// answer "what is installed, what is missing, and get me the missing one"
// without a shell. That matters more than it sounds on Windows: the scripts are
// PowerShell, and a user who has never changed the execution policy cannot run
// one. It is also the difference between a wall of text at the first attempt to
// dictate and a list with a button next to it.
//
// The catalogue is deliberately the same set of names and repositories the
// scripts use, and a test reads both scripts and fails if they drift.
package models

import (
	"fmt"

	"github.com/dennis2lee/local-dictation/client/internal/localserver"
)

// Role is what a model is for, which decides where its path belongs in the
// settings and whether its absence is a problem.
type Role int

const (
	// Accurate produces the text that is kept. Every backend needs one.
	Accurate Role = iota
	// Draft produces only the live partial text. Optional, and on a CPU it is
	// the difference between the first words appearing in five seconds and in
	// one.
	Draft
	// Detector decides when an utterance has ended. Optional — without it the
	// server falls back to an energy threshold.
	Detector
)

// Model is one downloadable directory, or in the detector's case one file.
type Model struct {
	// Name is the directory it installs into, and how the scripts refer to it.
	Name string
	// Repo is the HuggingFace repository. Empty for the detector, which comes
	// from its own project rather than from a model hub.
	Repo string
	// URL is the single file to fetch when Repo is empty.
	URL string
	// Backend is the one that reads this conversion. The detector serves all
	// of them, and carries the empty backend.
	Backend localserver.Backend
	Role    Role
	// Summary is the one line the Models tab shows.
	Summary string
	// Bytes is roughly how large the download is, for showing before it starts.
	// Approximate on purpose: the exact figure comes from the repository, and
	// asking for it would make opening a tab do network I/O.
	Bytes int64
}

const (
	mib = 1 << 20
	gib = 1 << 30
)

// SileroURL is where the voice activity detector comes from. Exported so the
// test that checks this catalogue against the shell scripts can compare it.
const SileroURL = "https://raw.githubusercontent.com/snakers4/silero-vad/master/src/silero_vad/data/silero_vad.onnx"

// Catalogue is every model this client knows how to install, in the order the
// Models tab lists them: the accurate models first, then what supports them.
func Catalogue() []Model {
	return []Model{
		{
			Name:    "large-v3-turbo",
			Repo:    "deepdml/faster-whisper-large-v3-turbo-ct2",
			Backend: localserver.BackendCPU,
			Role:    Accurate,
			Summary: "The usual choice on a CPU.",
			Bytes:   1546 * mib,
		},
		{
			Name:    "large-v3",
			Repo:    "Systran/faster-whisper-large-v3",
			Backend: localserver.BackendCPU,
			Role:    Accurate,
			Summary: "More accurate, several times slower.",
			Bytes:   2 * gib,
		},
		{
			Name:    "base",
			Repo:    "Systran/faster-whisper-base",
			Backend: localserver.BackendCPU,
			Role:    Draft,
			Summary: "Draft model: live text only, never kept.",
			Bytes:   140 * mib,
		},
		{
			Name:    "large-v3-turbo-openvino-int8",
			Repo:    "OpenVINO/whisper-large-v3-turbo-int8-ov",
			Backend: localserver.BackendIntelGPU,
			Role:    Accurate,
			Summary: "The usual choice on an Intel GPU.",
			Bytes:   790 * mib,
		},
		{
			Name:    "large-v3-turbo-openvino-fp16",
			Repo:    "OpenVINO/whisper-large-v3-turbo-fp16-ov",
			Backend: localserver.BackendIntelGPU,
			Role:    Accurate,
			Summary: "The accuracy reference to measure INT8 against.",
			Bytes:   1638 * mib,
		},
		{
			Name:    "large-v3-turbo-openvino-int4",
			Repo:    "OpenVINO/whisper-large-v3-turbo-int4-ov",
			Backend: localserver.BackendIntelGPU,
			Role:    Accurate,
			Summary: "Smallest and fastest; check Korean accuracy first.",
			Bytes:   600 * mib,
		},
		{
			Name:    "large-v3-turbo-mlx",
			Repo:    "mlx-community/whisper-large-v3-turbo",
			Backend: localserver.BackendAppleGPU,
			Role:    Accurate,
			Summary: "The usual choice on an Apple Silicon GPU.",
			Bytes:   1546 * mib,
		},
		{
			Name:    "silero_vad.onnx",
			URL:     SileroURL,
			Role:    Detector,
			Summary: "Decides when you have stopped speaking.",
			Bytes:   2214 * 1024,
		},
	}
}

// For returns the models a backend can use, in catalogue order.
func For(backend localserver.Backend) []Model {
	var wanted []Model
	for _, model := range Catalogue() {
		if model.Role == Detector || model.Backend.Normalise() == backend.Normalise() {
			wanted = append(wanted, model)
		}
	}
	return wanted
}

// Find returns the catalogue entry with this name.
func Find(name string) (Model, bool) {
	for _, model := range Catalogue() {
		if model.Name == name {
			return model, true
		}
	}
	return Model{}, false
}

// Weights names the file whose presence means this model is really installed.
//
// A directory is not enough: an interrupted download leaves one behind holding
// a tokenizer and no weights, which reads as installed and fails at load.
func (m Model) Weights() string {
	if m.Role == Detector {
		return m.Name
	}
	return m.Backend.Weights()
}

// IsFile reports whether this installs as a single file rather than a
// directory.
func (m Model) IsFile() bool { return m.Role == Detector }

// Size renders Bytes the way the fetch scripts do.
func Size(bytes int64) string {
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1f GB", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%d MB", bytes/mib)
	case bytes >= 1024:
		return fmt.Sprintf("%d KB", bytes/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
