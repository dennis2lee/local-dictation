package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/localserver"
)

// The knob for sentences nobody said, from the window it is turned in.
//
// It exists because the fix for a breath decoding as "감사합니다" is a
// threshold, and a threshold that is right for one room and one microphone is
// not right for the next. A field that shows the saved value and writes
// nothing back — or writes back without showing — reads as working until the
// next start, which is the failure the round trip is here to catch.
func TestTheSpeechGateSurvivesASave(t *testing.T) {
	settings := testSettings()
	settings.Local.MinSpeechMs = 250
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	if tab.minSpeech.Text != "250" {
		t.Errorf("the form opened showing %q, want the saved 250", tab.minSpeech.Text)
	}

	tab.minSpeech.SetText("400")
	if saved := tab.collect(settings); saved.Local.MinSpeechMs != 400 {
		t.Errorf("saved minimum speech = %d, want 400", saved.Local.MinSpeechMs)
	}
}

// Empty is the default, and the placeholder is where the user reads what the
// default is. Showing "0" instead would be a number nobody chose, and one that
// happens to look like the setting is off.
func TestAnEmptySpeechGateMeansTheDefault(t *testing.T) {
	settings := testSettings()
	settings.Local.MinSpeechMs = 0
	tab := newTestSettingsTab(halfBuiltApp(settings), settings)

	if tab.minSpeech.Text != "" {
		t.Errorf("the field shows %q for an unset gate, want it empty", tab.minSpeech.Text)
	}
	if want := strconv.Itoa(localserver.DefaultMinSpeechMs); !strings.Contains(
		tab.minSpeech.PlaceHolder, want) {
		t.Errorf("the placeholder %q does not say what the default is (%s)",
			tab.minSpeech.PlaceHolder, want)
	}
	if saved := tab.collect(settings); saved.Local.MinSpeechMs != 0 {
		t.Errorf("an empty field saved as %d, want 0", saved.Local.MinSpeechMs)
	}
}
