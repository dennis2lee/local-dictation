package localserver

import (
	"reflect"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// filled is a ManagerSettings with nothing left at its zero value, so that a
// field the manager forgets to pass on shows up as a zero on the other side.
func filled() ManagerSettings {
	return ManagerSettings{
		PythonPath:     "/usr/bin/python3",
		ServerDir:      "/opt/local-dictation/server",
		ModelPath:      "/models/large-v3-turbo",
		DraftModelPath: "/models/base",
		VadModelPath:   "/models/silero_vad.onnx",
		StateDir:       "/state",
		CPUThreads:     6,
		MinSpeechMs:    350,
		Backend:        BackendIntelGPU,
		KoreanPort:     8765,
		EnglishPort:    8766,
	}
}

// Every setting the manager holds has to reach the server it starts.
//
// The reflection is the point of the test rather than a shortcut around
// writing eleven comparisons: it fails for a field added later and never
// wired, which is the only way this breaks. A dropped setting is silent —
// the server runs on its own default and the settings window still shows what
// the user typed.
func TestEverySettingReachesTheServerItStarts(t *testing.T) {
	options := serverOptions(filled(), protocol.Korean, "/usr/bin/python3", "/opt/server").withDefaults()

	value := reflect.ValueOf(options)
	for i := range value.NumField() {
		if value.Field(i).IsZero() {
			t.Errorf("Options.%s is still zero, so whatever the user set for it never left the manager",
				value.Type().Field(i).Name)
		}
	}

	if options.MinSpeechMs != 350 {
		t.Errorf("MinSpeechMs reached the server as %d, want 350", options.MinSpeechMs)
	}
}

// The two ports are one field on the way in and one on the way out, which is
// the only place in the hop where a value is chosen rather than copied.
func TestEachLanguageServerGetsItsOwnPort(t *testing.T) {
	settings := filled()

	if got := serverOptions(settings, protocol.Korean, "", "").Port; got != settings.KoreanPort {
		t.Errorf("the Korean server was started on %d, want %d", got, settings.KoreanPort)
	}
	if got := serverOptions(settings, protocol.English, "", "").Port; got != settings.EnglishPort {
		t.Errorf("the English server was started on %d, want %d", got, settings.EnglishPort)
	}
}
