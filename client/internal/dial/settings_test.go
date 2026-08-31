package dial

import (
	"reflect"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/localserver"
)

// Every local setting has to survive the hop out of the config file.
//
// This is the first of two copies between what the user saves and what the
// server is told — see TestEverySettingReachesTheServerItStarts for the
// second. Neither has anything to report when it drops a field, so the
// reflection is what makes the omission visible: a knob added to Local and
// forgotten here still saves, still shows what was typed, and does nothing.
func TestEveryLocalSettingReachesTheServerManager(t *testing.T) {
	settings := config.Default()
	settings.Mode = config.ModeLocal
	settings.Local = config.Local{
		PythonPath:         "/usr/bin/python3",
		ServerDir:          "/opt/local-dictation/server",
		ModelPath:          "/models/large-v3-turbo",
		DraftModelPath:     "/models/base",
		VadModelPath:       "/models/silero_vad.onnx",
		KoreanPort:         8765,
		EnglishPort:        8766,
		StartBothLanguages: true,
		CPUThreads:         6,
		MinSpeechMs:        350,
		Backend:            localserver.BackendIntelGPU,
	}

	carried := managerSettings(settings, "/state")

	value := reflect.ValueOf(carried)
	for i := range value.NumField() {
		if value.Field(i).IsZero() {
			t.Errorf("ManagerSettings.%s is still zero, so the setting behind it never left the config",
				value.Type().Field(i).Name)
		}
	}

	if carried.MinSpeechMs != settings.Local.MinSpeechMs {
		t.Errorf("minimum speech left the config as %d, want %d",
			carried.MinSpeechMs, settings.Local.MinSpeechMs)
	}
}
