package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

func localSettings() Config {
	settings := Default()
	settings.Mode = ModeLocal
	settings.Local.ModelPath = "/models/large-v3-turbo"
	return settings
}

// 0 is not "unset by mistake": it is how the client asks for a free port, which
// is what keeps a standalone session working on a machine already using 8765.
func TestStandalonePortsDefaultToChoosingAFreeOne(t *testing.T) {
	settings := localSettings()
	if settings.Local.KoreanPort != 0 || settings.Local.EnglishPort != 0 {
		t.Fatalf("expected both standalone ports to default to 0, got %d and %d",
			settings.Local.KoreanPort, settings.Local.EnglishPort)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("the defaults should validate: %v", err)
	}
}

func TestStandalonePortsMayBePinned(t *testing.T) {
	settings := localSettings()
	settings.Local.KoreanPort, settings.Local.EnglishPort = 9001, 9002
	if err := settings.Validate(); err != nil {
		t.Fatalf("pinned ports should validate: %v", err)
	}
	if got := settings.PortFor(protocol.Korean); got != 9001 {
		t.Errorf("PortFor(ko) = %d, want 9001", got)
	}
	if got := settings.PortFor(protocol.English); got != 9002 {
		t.Errorf("PortFor(en) = %d, want 9002", got)
	}
}

func TestTwoStandaloneServersCannotSharePinnedPort(t *testing.T) {
	settings := localSettings()
	settings.Local.KoreanPort, settings.Local.EnglishPort = 9001, 9001
	err := settings.Validate()
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("expected the shared port to be rejected, got %v", err)
	}
}

// Both zero is two requests for a free port, not one port used twice.
func TestBothStandalonePortsMayBeAutomatic(t *testing.T) {
	settings := localSettings()
	settings.Local.KoreanPort, settings.Local.EnglishPort = 0, 0
	if err := settings.Validate(); err != nil {
		t.Fatalf("two automatic ports should validate: %v", err)
	}
}

func TestStandalonePortOutOfRangeIsRejected(t *testing.T) {
	settings := localSettings()
	settings.Local.KoreanPort = 70000
	err := settings.Validate()
	if err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("expected an out-of-range port to be rejected, got %v", err)
	}
}

// Remote is the opposite rule: there is nothing to pick a free port on.
func TestRemotePortsAreRequired(t *testing.T) {
	settings := Default()
	settings.Mode = ModeRemote
	settings.Remote.Host = "dictation.internal"
	settings.Remote.KoreanPort, settings.Remote.EnglishPort = 0, 0
	err := settings.Validate()
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("expected missing remote ports to be rejected, got %v", err)
	}
}

func TestStandalonePortsSurviveSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := localSettings()
	settings.Local.KoreanPort, settings.Local.EnglishPort = 9101, 9102
	if err := settings.SaveTo(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Local.KoreanPort != 9101 || loaded.Local.EnglishPort != 9102 {
		t.Errorf("ports did not round-trip: got %d and %d",
			loaded.Local.KoreanPort, loaded.Local.EnglishPort)
	}
}

// The gate on sentences nobody said, from the settings window's side.
//
// 0 is not off: it means the server's own default, which is on. Turning the
// gate off is done by writing that into the server config by hand, because the
// state it restores — a breath opening an utterance, and the decode of that
// breath coming back as "감사합니다" — is not one to reach by leaving a field
// empty.
func TestTheMinimumSpeechDefaultIsTheServersOwn(t *testing.T) {
	settings := localSettings()
	if settings.Local.MinSpeechMs != 0 {
		t.Errorf("MinSpeechMs defaults to %d; 0 is what defers to the server",
			settings.Local.MinSpeechMs)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("the default should validate: %v", err)
	}
}

func TestTheMinimumSpeechIsHeldToWhatAWordMeasures(t *testing.T) {
	// The shortest real Korean word measures about 290 ms of detected speech,
	// so a gate anywhere near the ceiling drops words the user said — and a
	// negative one is not a duration at all.
	for _, ms := range []int{-1, 1001, 5000} {
		settings := localSettings()
		settings.Local.MinSpeechMs = ms
		err := settings.Validate()
		if err == nil {
			t.Errorf("a minimum speech of %d ms was accepted", ms)
			continue
		}
		if !strings.Contains(err.Error(), "minimum speech") {
			t.Errorf("the complaint about %d ms does not say what is wrong: %v", ms, err)
		}
	}

	for _, ms := range []int{0, 120, 1000} {
		settings := localSettings()
		settings.Local.MinSpeechMs = ms
		if err := settings.Validate(); err != nil {
			t.Errorf("a minimum speech of %d ms was rejected: %v", ms, err)
		}
	}
}

func TestTheMinimumSpeechSurvivesSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := localSettings()
	settings.Local.MinSpeechMs = 350
	if err := settings.SaveTo(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Local.MinSpeechMs != 350 {
		t.Errorf("minimum speech did not round-trip: got %d, want 350", loaded.Local.MinSpeechMs)
	}
}
