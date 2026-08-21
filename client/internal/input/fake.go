package input

import (
	"errors"
	"strings"
	"sync"
)

// FakePlatform is a text field in memory. It models the one behaviour that
// matters: marked text is provisional and gets replaced wholesale, committed
// text is permanent. Tests assert on Document(), which is what the user would
// actually see.
type FakePlatform struct {
	mu        sync.Mutex
	committed strings.Builder
	marked    string
	composing bool
	closed    bool

	// Failures to inject.
	FailBegin  error
	FailCommit error
	FailMark   error

	// Call counters, for asserting that we do not thrash the IME.
	Begins  int
	Commits int
	Marks   int
	Ends    int
	Cancels int
}

func NewFakePlatform() *FakePlatform { return &FakePlatform{} }

func (f *FakePlatform) Name() string { return "fake" }

// Document is what the user sees: committed text plus the underlined region.
func (f *FakePlatform) Document() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.committed.String() + f.marked
}

// Committed is only the permanent text.
func (f *FakePlatform) Committed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.committed.String()
}

func (f *FakePlatform) Marked() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marked
}

func (f *FakePlatform) Composing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.composing
}

func (f *FakePlatform) BeginComposition() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailBegin != nil {
		return f.FailBegin
	}
	if f.composing {
		return errors.New("already composing")
	}
	f.composing = true
	f.Begins++
	return nil
}

func (f *FakePlatform) SetMarkedText(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailMark != nil {
		return f.FailMark
	}
	if !f.composing {
		return errors.New("not composing")
	}
	f.marked = text
	f.Marks++
	return nil
}

func (f *FakePlatform) CommitText(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailCommit != nil {
		return f.FailCommit
	}
	if !f.composing {
		return errors.New("not composing")
	}
	f.committed.WriteString(text)
	f.Commits++
	return nil
}

func (f *FakePlatform) EndComposition() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.composing {
		return nil
	}
	// Marked text that survives to EndComposition becomes real text.
	f.committed.WriteString(f.marked)
	f.marked = ""
	f.composing = false
	f.Ends++
	return nil
}

func (f *FakePlatform) CancelComposition() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = ""
	f.composing = false
	f.Cancels++
	return nil
}

func (f *FakePlatform) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *FakePlatform) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
