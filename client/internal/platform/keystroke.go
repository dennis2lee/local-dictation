package platform

import (
	"sync"
	"unicode/utf8"
)

// keystrokeComposer emulates an IME composition with ordinary typing.
//
// It tracks how much provisional text it has typed so it knows how many
// backspaces to send. The count is in runes, not bytes: a backspace deletes one
// grapheme's worth of code point, and Korean syllables are three bytes each.
type keystrokeComposer struct {
	mu     sync.Mutex
	typer  typer
	marked string
	open   bool
}

// typer is the OS primitive: insert a string, or delete N characters back.
type typer interface {
	typeText(text string) error
	backspace(count int) error
	name() string
	close() error
}

func (k *keystrokeComposer) Name() string { return k.typer.name() }

func (k *keystrokeComposer) BeginComposition() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.open = true
	k.marked = ""
	return nil
}

// SetMarkedText replaces the provisional region.
//
// The common case by far is that the new text extends the old one — Whisper
// adds words far more often than it rewrites them — so only the differing tail
// is retyped. That matters: retyping the whole hypothesis every 600 ms would
// send hundreds of keystrokes a second into the focused application.
func (k *keystrokeComposer) SetMarkedText(text string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if text == k.marked {
		return nil
	}

	shared := commonPrefix(k.marked, text)
	if remove := utf8.RuneCountInString(k.marked) - utf8.RuneCountInString(shared); remove > 0 {
		if err := k.typer.backspace(remove); err != nil {
			return err
		}
	}
	if addition := text[len(shared):]; addition != "" {
		if err := k.typer.typeText(addition); err != nil {
			// The provisional region is now whatever actually got typed. Being
			// honest about it means the next call fixes it up rather than
			// backspacing over the user's own text.
			k.marked = shared
			return err
		}
	}
	k.marked = text
	return nil
}

// CommitText inserts permanent text ahead of the provisional region.
//
// With synthetic input there is no such thing as "ahead of": everything is real
// text in document order. So the provisional tail is removed, the committed
// text typed, and the composer's next SetMarkedText call puts the tail back.
func (k *keystrokeComposer) CommitText(text string) error {
	if text == "" {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	if count := utf8.RuneCountInString(k.marked); count > 0 {
		if err := k.typer.backspace(count); err != nil {
			return err
		}
		k.marked = ""
	}
	return k.typer.typeText(text)
}

// EndComposition keeps whatever is on screen: it is already real text.
func (k *keystrokeComposer) EndComposition() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.marked = ""
	k.open = false
	return nil
}

// CancelComposition removes the provisional text.
func (k *keystrokeComposer) CancelComposition() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	count := utf8.RuneCountInString(k.marked)
	k.marked = ""
	k.open = false
	if count == 0 {
		return nil
	}
	return k.typer.backspace(count)
}

func (k *keystrokeComposer) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if count := utf8.RuneCountInString(k.marked); count > 0 {
		_ = k.typer.backspace(count)
		k.marked = ""
	}
	return k.typer.close()
}

func commonPrefix(a, b string) string {
	limit := min(len(a), len(b))
	last := 0
	for index := 0; index < limit; {
		runeA, sizeA := utf8.DecodeRuneInString(a[index:])
		runeB, sizeB := utf8.DecodeRuneInString(b[index:])
		if runeA != runeB || sizeA != sizeB {
			break
		}
		index += sizeA
		last = index
	}
	return a[:last]
}
