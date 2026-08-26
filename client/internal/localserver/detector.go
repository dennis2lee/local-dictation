package localserver

import (
	"os"
	"path/filepath"
	"strings"
)

// DetectorName is the file the server loads its voice detector from.
//
// Both ways of getting a model put it in the model's own directory:
// server/scripts/fetch-model.sh downloads it alongside every model it fetches,
// and Settings › Models installs it there. So by the time anyone has something
// to dictate with, this file is almost always already on disk.
const DetectorName = "silero_vad.onnx"

// ResolveDetector picks the voice detector a local server should use.
//
// An explicit setting wins when it names a file that is there. Otherwise this
// looks beside the model — which is where both installers put it, and where it
// was sitting unused: the setting that names it is blank in every install that
// never had it typed in by hand, and a blank setting used to mean the energy
// detector even with the real one in the same directory as the weights.
//
// Falling back to the energy detector is not a graceful degradation. It is an
// RMS comparison, and measured against this project's own clips it calls a
// breath speech for 0.38 s where the word "네" measures 0.24 s. It ranks a
// breath above a real word, so no threshold above it can separate the two, and
// a window of breath reaches the decoder — which does not answer a window with
// no speech in it with silence. It answers with the boilerplate its training
// subtitles ended on, which on Korean audio is reliably "감사합니다".
//
// An empty return means there is none to be found, and the caller should say
// so rather than start a server that will invent sentences.
func ResolveDetector(configured, modelPath string) string {
	candidates := []string{strings.TrimSpace(configured)}
	if model := strings.TrimSpace(modelPath); model != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(model), DetectorName))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
