package input

import (
	"errors"
	"strings"
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

func transcript(utterance string, revision int64, stable, partial string, final bool) protocol.Transcript {
	return protocol.Transcript{
		Type:            "transcript",
		ProtocolVersion: protocol.Version,
		UtteranceID:     utterance,
		Revision:        revision,
		Stable:          stable,
		Partial:         partial,
		Final:           final,
	}
}

// The sequence a real Korean utterance produces, taken from the server's own
// output: stable grows one eojeol at a time and carries its trailing space.
var koreanStream = []protocol.Transcript{
	transcript("u-1", 1, "", "오늘", false),
	transcript("u-1", 2, "", "오늘 오후", false),
	transcript("u-1", 3, "오늘 ", "오후 세", false),
	transcript("u-1", 4, "오늘 오후 ", "세 시에", false),
	transcript("u-1", 5, "오늘 오후 세 ", "시에 회의를", false),
	transcript("u-1", 6, "오늘 오후 세 시에 ", "회의를", false),
	transcript("u-1", 7, "오늘 오후 세 시에 회의를", "", true),
}

func TestTheUserSeesTheGrowingHypothesisAtEveryStep(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	for _, event := range koreanStream {
		if err := composer.Apply(event); err != nil {
			t.Fatalf("apply revision %d: %v", event.Revision, err)
		}
		if got, want := platform.Document(), event.Text(); got != want {
			t.Errorf("revision %d: document = %q, want %q", event.Revision, got, want)
		}
	}
	if got := platform.Committed(); got != "오늘 오후 세 시에 회의를" {
		t.Errorf("final committed text = %q", got)
	}
	if platform.Composing() {
		t.Error("composition still open after the final event")
	}
}

func TestNothingIsCommittedTwice(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)
	for _, event := range koreanStream {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	document := platform.Document()
	if count := strings.Count(document, "오늘"); count != 1 {
		t.Errorf("%q contains 오늘 %d times, want 1", document, count)
	}
	if count := strings.Count(document, "회의를"); count != 1 {
		t.Errorf("%q contains 회의를 %d times, want 1", document, count)
	}
}

func TestOnlyChangedStableTextIsCommitted(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)
	for _, event := range koreanStream {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	// Five stable growth steps in this stream; anything more means we are
	// re-committing text the document already has.
	if platform.Commits != 5 {
		t.Errorf("committed %d times, want 5", platform.Commits)
	}
	if platform.Begins != 1 {
		t.Errorf("began composition %d times, want 1", platform.Begins)
	}
}

func TestStaleRevisionsAreDiscarded(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	for _, event := range koreanStream[:5] {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	before := platform.Document()

	// A duplicate and an out-of-order event, as a reconnect can produce.
	for _, revision := range []int64{5, 3, 1} {
		if err := composer.Apply(transcript("u-1", revision, "완전히 다른", "", false)); err != nil {
			t.Fatalf("stale revision %d should be ignored, got %v", revision, err)
		}
	}
	if got := platform.Document(); got != before {
		t.Errorf("stale events changed the document: %q -> %q", before, got)
	}
	if composer.StaleDiscarded != 3 {
		t.Errorf("StaleDiscarded = %d, want 3", composer.StaleDiscarded)
	}
}

func TestANewUtteranceStartsAFreshPrefix(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	if err := composer.Apply(transcript("u-1", 1, "hello world", "", true)); err != nil {
		t.Fatal(err)
	}
	if err := composer.Apply(transcript("u-2", 2, "", "second", false)); err != nil {
		t.Fatal(err)
	}
	if err := composer.Apply(transcript("u-2", 3, "second sentence", "", true)); err != nil {
		t.Fatal(err)
	}
	if got, want := platform.Committed(), "hello worldsecond sentence"; got != want {
		t.Errorf("committed = %q, want %q", got, want)
	}
	if composer.UtterancesShown != 2 {
		t.Errorf("UtterancesShown = %d, want 2", composer.UtterancesShown)
	}
}

func TestDropPartialKeepsCommittedTextAndLosesTheGuess(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	for _, event := range koreanStream[:5] {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	committed := platform.Committed()
	if committed == "" {
		t.Fatal("nothing committed yet; the test needs some")
	}

	if err := composer.DropPartial(); err != nil {
		t.Fatal(err)
	}
	if got := platform.Document(); got != committed {
		t.Errorf("document = %q, want the committed prefix %q", got, committed)
	}
	if platform.Marked() != "" {
		t.Errorf("marked text survived DropPartial: %q", platform.Marked())
	}
}

func TestEventsForAnAbandonedUtteranceAreIgnored(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	for _, event := range koreanStream[:5] {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := composer.DropPartial(); err != nil {
		t.Fatal(err)
	}
	committed := platform.Committed()

	// A late event for the torn-down utterance would otherwise re-commit its
	// whole stable prefix on top of text the document already holds.
	if err := composer.Apply(koreanStream[6]); err != nil {
		t.Fatal(err)
	}
	if got := platform.Document(); got != committed {
		t.Errorf("late event duplicated text: %q, want %q", got, committed)
	}
}

func TestARetractedPrefixIsRefused(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	for _, event := range koreanStream[:5] {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	committed := platform.Committed()

	err := composer.Apply(transcript("u-1", 99, "완전히 다른 문장", "", false))
	if !errors.Is(err, ErrStablePrefixChanged) {
		t.Fatalf("err = %v, want ErrStablePrefixChanged", err)
	}
	if got := platform.Committed(); got != committed {
		t.Errorf("committed text was altered: %q, want %q", got, committed)
	}
}

func TestFinishCommitsMarkedTextWhenTheServerNeverSaidFinal(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)

	for _, event := range koreanStream[:6] {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	hypothesis := platform.Document()

	if err := composer.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := platform.Committed(); got != hypothesis {
		t.Errorf("committed = %q, want the whole hypothesis %q", got, hypothesis)
	}
	if platform.Composing() {
		t.Error("composition left open")
	}
}

func TestResetDoesNotTouchTheDocument(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)
	for _, event := range koreanStream {
		if err := composer.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	before := platform.Document()
	composer.Reset()
	if got := platform.Document(); got != before {
		t.Errorf("Reset changed the document: %q -> %q", before, got)
	}
	// After a reset the revision counter must not swallow a new session's
	// events, which start again from 1.
	if err := composer.Apply(transcript("u-9", 1, "", "next", false)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(platform.Document(), "next") {
		t.Errorf("document = %q, want it to end with the new partial", platform.Document())
	}
}

func TestAPlatformFailureIsReportedNotSwallowed(t *testing.T) {
	platform := NewFakePlatform()
	platform.FailCommit = errors.New("TSF said no")
	composer := NewComposer(platform)

	err := composer.Apply(transcript("u-1", 1, "hello ", "world", false))
	if err == nil || !strings.Contains(err.Error(), "TSF said no") {
		t.Fatalf("err = %v, want the platform error to surface", err)
	}
}

func TestCloseCancelsAnOpenComposition(t *testing.T) {
	platform := NewFakePlatform()
	composer := NewComposer(platform)
	if err := composer.Apply(koreanStream[0]); err != nil {
		t.Fatal(err)
	}
	if err := composer.Close(); err != nil {
		t.Fatal(err)
	}
	if platform.Composing() {
		t.Error("composition left open after Close")
	}
	if !platform.Closed() {
		t.Error("platform was not closed")
	}
}
