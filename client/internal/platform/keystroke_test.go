package platform

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dennis2lee/local-dictation/client/internal/input"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// recordingTyper is a text field plus a keystroke counter, so tests can assert
// both what the user ends up seeing and how much typing it took to get there.
// previewComposer turns the volatile tail back on. Off by default — see
// config.Input.LivePreview — and these tests are what describe it.
func previewComposer(c *input.Composer) *input.Composer {
	c.SetLivePreview(true)
	return c
}

type recordingTyper struct {
	text       []rune
	typed      int
	backspaces int
	calls      int
	failNext   error
}

func (r *recordingTyper) typeText(text string) error {
	r.calls++
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.text = append(r.text, []rune(text)...)
	r.typed += utf8.RuneCountInString(text)
	return nil
}

func (r *recordingTyper) backspace(count int) error {
	r.calls++
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	if count > len(r.text) {
		// A real keyboard would eat the user's own text here. Failing loudly in
		// tests is the only way to catch an accounting bug before it ships.
		panic("backspaced past the start of the document")
	}
	r.text = r.text[:len(r.text)-count]
	r.backspaces += count
	return nil
}

func (r *recordingTyper) name() string   { return "recording" }
func (r *recordingTyper) close() error   { return nil }
func (r *recordingTyper) String() string { return string(r.text) }

func newComposer() (*input.Composer, *recordingTyper) {
	typer := &recordingTyper{}
	return previewComposer(input.NewComposer(&keystrokeComposer{typer: typer})), typer
}

func transcript(revision int64, stable, partial string, final bool) protocol.Transcript {
	return protocol.Transcript{
		Type: "transcript", ProtocolVersion: 1, UtteranceID: "u-1",
		Revision: revision, Stable: stable, Partial: partial, Final: final,
	}
}

func TestTheDocumentTracksTheHypothesisThroughSyntheticTyping(t *testing.T) {
	composer, typer := newComposer()
	stream := []protocol.Transcript{
		transcript(1, "", "오늘", false),
		transcript(2, "", "오늘 오후", false),
		transcript(3, "오늘 ", "오후 세", false),
		transcript(4, "오늘 오후 ", "세 시에", false),
		transcript(5, "오늘 오후 세 시에 ", "회의를", false),
		transcript(6, "오늘 오후 세 시에 회의를", "", true),
	}
	for _, event := range stream {
		if err := composer.Apply(event); err != nil {
			t.Fatalf("revision %d: %v", event.Revision, err)
		}
		if got, want := typer.String(), event.Text(); got != want {
			t.Errorf("revision %d: document = %q, want %q", event.Revision, got, want)
		}
	}
}

func TestOnlyTheChangedTailIsRetyped(t *testing.T) {
	composer, typer := newComposer()
	// A hypothesis that only ever grows should cost one keystroke per new
	// character, not a full retype of the sentence on every revision.
	for revision, partial := range []string{"a", "ab", "abc", "abcd", "abcde"} {
		if err := composer.Apply(transcript(int64(revision+1), "", partial, false)); err != nil {
			t.Fatal(err)
		}
	}
	if typer.typed != 5 {
		t.Errorf("typed %d characters for a 5-character hypothesis, want 5", typer.typed)
	}
	if typer.backspaces != 0 {
		t.Errorf("sent %d backspaces for a purely growing hypothesis, want 0", typer.backspaces)
	}
}

func TestAReplacedTailBackspacesOnlyWhatChanged(t *testing.T) {
	composer, typer := newComposer()
	if err := composer.Apply(transcript(1, "", "three thirty", false)); err != nil {
		t.Fatal(err)
	}
	before := typer.backspaces
	if err := composer.Apply(transcript(2, "", "three forty", false)); err != nil {
		t.Fatal(err)
	}
	// "three " is shared; only "thirty" (6 characters) needs removing.
	if delta := typer.backspaces - before; delta != 6 {
		t.Errorf("backspaced %d characters, want 6", delta)
	}
	if got := typer.String(); got != "three forty" {
		t.Errorf("document = %q", got)
	}
}

func TestBackspaceCountsRunesNotBytes(t *testing.T) {
	composer, typer := newComposer()
	// Each Korean syllable is three bytes; a byte-based count would delete the
	// user's own text.
	if err := composer.Apply(transcript(1, "", "안녕하세요", false)); err != nil {
		t.Fatal(err)
	}
	if err := composer.Apply(transcript(2, "", "안녕히", false)); err != nil {
		t.Fatal(err)
	}
	if got := typer.String(); got != "안녕히" {
		t.Errorf("document = %q, want 안녕히", got)
	}
}

func TestCommittedTextSurvivesAPartialBeingDropped(t *testing.T) {
	composer, typer := newComposer()
	if err := composer.Apply(transcript(1, "", "hello", false)); err != nil {
		t.Fatal(err)
	}
	if err := composer.Apply(transcript(2, "hello ", "world", false)); err != nil {
		t.Fatal(err)
	}
	if got := typer.String(); got != "hello world" {
		t.Fatalf("document = %q", got)
	}
	if err := composer.DropPartial(); err != nil {
		t.Fatal(err)
	}
	if got := typer.String(); got != "hello " {
		t.Errorf("document = %q, want the committed prefix %q", got, "hello ")
	}
}

func TestATypingFailureLeavesAConsistentView(t *testing.T) {
	typer := &recordingTyper{}
	composer := previewComposer(input.NewComposer(&keystrokeComposer{typer: typer}))

	if err := composer.Apply(transcript(1, "", "hello", false)); err != nil {
		t.Fatal(err)
	}
	typer.failNext = errors.New("SendInput refused")
	err := composer.Apply(transcript(2, "", "hello there", false))
	if err == nil || !strings.Contains(err.Error(), "SendInput refused") {
		t.Fatalf("err = %v, want the typing failure to surface", err)
	}
	// The next successful update must not backspace over text that was never
	// typed, which is what the panic in recordingTyper.backspace guards.
	if err := composer.Apply(transcript(3, "", "hello there now", false)); err != nil {
		t.Fatal(err)
	}
	if got := typer.String(); got != "hello there now" {
		t.Errorf("document = %q after recovery", got)
	}
}

func TestCommonPrefixHandlesMultibyteText(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", ""},
		{"abc", "abd", "ab"},
		{"오늘 오후", "오늘 저녁", "오늘 "},
		{"안녕", "안녕하세요", "안녕"},
		{"🙂🙃", "🙂😀", "🙂"},
	}
	for _, c := range cases {
		if got := commonPrefix(c.a, c.b); got != c.want {
			t.Errorf("commonPrefix(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// -- the default: nothing is typed until it has settled ---------------------

// The sequence a Korean sentence actually produces: stable grows one eojeol at
// a time and the tail keeps being rewritten.
var settlingStream = []protocol.Transcript{
	transcript(1, "", "오늘", false),
	transcript(2, "", "오늘 오후", false),
	transcript(3, "오늘 ", "오후 세", false),
	transcript(4, "오늘 오후 ", "세 시에", false),
	transcript(5, "오늘 오후 세 시에 ", "회의를", false),
	transcript(6, "오늘 오후 세 시에 회의를", "", true),
}

// The complaint that changed this: dictation could not keep up with a fast
// speaker. Every decode pass backspaced the whole volatile tail, typed the
// words that had just settled, and typed the tail back — a burst of synthetic
// keystrokes six hundred milliseconds apart, each one a round trip through the
// window server. With the tail hidden, the same utterance is only appended to.
func TestNothingIsBackspacedWhenTheTailIsNotShown(t *testing.T) {
	typer := &recordingTyper{}
	composer := input.NewComposer(&keystrokeComposer{typer: typer}) // preview off

	for _, event := range settlingStream {
		if err := composer.Apply(event); err != nil {
			t.Fatalf("revision %d: %v", event.Revision, err)
		}
	}

	if typer.backspaces != 0 {
		t.Errorf("backspaced %d characters with the tail hidden", typer.backspaces)
	}
	if got := typer.String(); got != "오늘 오후 세 시에 회의를" {
		t.Errorf("document = %q", got)
	}
}

// Every character of the sentence is typed exactly once, and the preview path
// is measurably the expensive one. The log line is the number the change was
// made for.
func TestEachCharacterIsTypedOnce(t *testing.T) {
	quiet := &recordingTyper{}
	composer := input.NewComposer(&keystrokeComposer{typer: quiet})
	for _, event := range settlingStream {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}

	final := "오늘 오후 세 시에 회의를"
	if quiet.typed != len([]rune(final)) {
		t.Errorf("typed %d characters for a %d-character sentence",
			quiet.typed, len([]rune(final)))
	}

	loud := &recordingTyper{}
	preview := previewComposer(input.NewComposer(&keystrokeComposer{typer: loud}))
	for _, event := range settlingStream {
		if err := preview.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	if loud.typed+loud.backspaces <= quiet.typed+quiet.backspaces {
		t.Errorf("preview cost %d keystrokes and the quiet path %d; "+
			"the preview is supposed to be the expensive one",
			loud.typed+loud.backspaces, quiet.typed+quiet.backspaces)
	}
	t.Logf("one %d-character sentence: preview %d typed + %d backspaced, "+
		"settled-only %d typed + %d backspaced",
		len([]rune(final)), loud.typed, loud.backspaces, quiet.typed, quiet.backspaces)
}
