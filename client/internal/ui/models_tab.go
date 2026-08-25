package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/localserver"
	"github.com/dennis2lee/local-dictation/client/internal/models"
)

// buildModelsSection is the Models group: what is installed, what the chosen
// backend still needs, and a button that fetches it.
//
// It exists because the alternative was a PowerShell script. Every other
// prerequisite this app has can be checked and fixed from inside it; the models
// — the largest and the only one nothing works without — were a command in a
// document, and on Windows one that a default execution policy refuses to run.
func (s *settingsTab) buildModelsSection(settings config.Config) fyne.CanvasObject {
	s.modelsBox = container.NewVBox()
	s.modelsWhere = inlineCaption("")
	// A VBox hands every child the width of its widest, so one long path here
	// stretches every row under it and pushes their buttons off the edge. The
	// directory is long by nature — it is somewhere under AppData — so it is
	// the tail that goes, not the layout.
	s.modelsWhere.Truncation = fyne.TextTruncateEllipsis
	s.modelsStatus = widget.NewLabel("")
	s.modelsStatus.Wrapping = fyne.TextWrapWord
	s.modelsStatus.Hide()
	s.modelsProgress = widget.NewProgressBar()
	s.modelsProgress.Hide()

	if s.installModel == nil {
		s.installModel = models.Install
	}

	s.refreshModels()
	return container.NewVBox(
		s.modelsWhere,
		s.modelsBox,
		s.modelsProgress,
		s.modelsStatus,
	)
}

// modelsDir is where models are kept: beside the one the settings already name,
// so a download lands where this client is already looking rather than where a
// script's default would have put it.
func (s *settingsTab) modelsDir() string {
	if configured := strings.TrimSpace(s.modelPath.Text); configured != "" {
		return filepath.Dir(configured)
	}
	if fallback := config.DefaultModelPath(); fallback != "" {
		return filepath.Dir(fallback)
	}
	return ""
}

// refreshModels rebuilds the list against what is on disk right now.
func (s *settingsTab) refreshModels() {
	directory := s.modelsDir()
	s.modelsWhere.SetText("Installed in " + directory)
	s.modelsBox.RemoveAll()

	backend := backendFor(s.backend.Selected)
	needed := map[string]bool{}
	for _, model := range models.For(backend) {
		needed[model.Name] = true
	}

	// The chosen backend's models first and under a heading that names it,
	// because "which of these do I actually need" is the question someone
	// arrives at this tab with. The rest are still listed — that is the other
	// half of the question — but below.
	var mine, others []models.State
	// Whether this backend has anything at all to decode with. Several models
	// can fill that role — large-v3-turbo and large-v3 both do — so "missing"
	// is a property of the set, not of each one. Marking every uninstalled
	// alternative in red would say four things are wrong when nothing is.
	served := false
	for _, state := range models.Scan(directory) {
		if !needed[state.Model.Name] {
			others = append(others, state)
			continue
		}
		mine = append(mine, state)
		served = served || (state.Installed && state.Model.Role == models.Accurate)
	}

	s.modelsBox.Add(inlineCaption("For " + backend.Label()))
	for _, state := range mine {
		s.modelsBox.Add(s.modelRow(state, !served))
	}
	if len(others) > 0 {
		s.modelsBox.Add(inlineCaption("Other backends"))
		for _, state := range others {
			s.modelsBox.Add(s.modelRow(state, false))
		}
	}
	s.modelsBox.Refresh()
}

// modelRow is one line: what it is, whether it is here, and a button.
//
// `urgent` says this backend has no accurate model installed at all, which is
// the one state on this tab that stops dictation working.
func (s *settingsTab) modelRow(state models.State, urgent bool) fyne.CanvasObject {
	name := widget.NewLabel(state.Model.Name)
	// Truncated, not wrapped and not left at its natural width. A Border gives
	// its centre whatever the sides do not take, but a label that insists on
	// its full width makes the row wider than the window and pushes the button
	// off the right-hand edge — where it is still tappable and no longer
	// visible. Ellipsis is the honest way to lose the tail of a sentence whose
	// first half already says which model this is.
	detail := inlineCaption(modelDetail(state))
	detail.Truncation = fyne.TextTruncateEllipsis

	switch {
	case state.Installed:
		name.Importance = widget.SuccessImportance
	case urgent && state.Model.Role == models.Accurate:
		// Nothing here can decode. Everything else on this tab is optional, an
		// alternative to something already installed, or belongs to a backend
		// that is not selected.
		name.Importance = widget.DangerImportance
	default:
		name.Importance = widget.MediumImportance
	}

	action := widget.NewButton(buttonLabel(state), nil)
	model := state.Model
	action.OnTapped = func() { s.onInstallModel(model) }
	if s.installing {
		action.Disable()
	}

	return container.NewBorder(nil, nil, name, action, detail)
}

func modelDetail(state models.State) string {
	if state.Installed {
		return models.Size(state.Bytes) + " · " + state.Model.Summary
	}
	return models.Size(state.Model.Bytes) + " · " + state.Model.Summary
}

func buttonLabel(state models.State) string {
	if state.Installed {
		return "Re-download"
	}
	return "Download"
}

// onInstallModel fetches one model, reporting progress, and leaves the rest of
// the tab usable but not clickable while it runs.
func (s *settingsTab) onInstallModel(model models.Model) {
	if s.installing {
		return
	}
	directory := s.modelsDir()
	if directory == "" {
		s.sayModels("There is nowhere to install to: set a model directory on the Local server tab first.")
		return
	}

	s.installing = true
	s.refreshModels()
	s.modelsProgress.SetValue(0)
	s.modelsProgress.Show()
	s.sayModels(fmt.Sprintf("Downloading %s (%s)…", model.Name, models.Size(model.Bytes)))

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelInstall = cancel

	go func() {
		defer cancel()
		err := s.installModel(ctx, model, directory, func(p models.Progress) {
			fyne.Do(func() {
				if p.TotalBytes > 0 {
					s.modelsProgress.SetValue(float64(p.Bytes) / float64(p.TotalBytes))
				}
				s.sayModels(fmt.Sprintf("Downloading %s — %s (%d/%d)",
					model.Name, p.File, p.Index, p.Count))
			})
		})
		fyne.Do(func() { s.finishInstall(model, directory, err) })
	}()
}

func (s *settingsTab) finishInstall(model models.Model, directory string, err error) {
	s.installing = false
	s.cancelInstall = nil
	s.modelsProgress.Hide()

	if err != nil {
		s.sayModels(fmt.Sprintf("Could not download %s: %v", model.Name, err))
		s.refreshModels()
		return
	}

	message := model.Name + " is installed."
	// Close the loop that made this tab necessary: a model downloaded for the
	// selected backend, while Model directory still points at another one, is
	// exactly the state that fails at the first attempt to dictate. It is not
	// saved here — Save is still theirs to press — but the field stops being
	// wrong.
	if s.adoptModel(model, directory) {
		message += " Model directory now points at it. Press Save to keep that."
	}
	s.sayModels(message)
	s.refreshModels()
	s.onBackendChanged(s.backend.Selected)
}

// adoptModel points Model directory at what was just installed, but only when
// the field is not already serving the chosen backend.
//
// `directory` is passed in rather than read back from modelsDir(), which is
// derived from the field this is about to rewrite.
func (s *settingsTab) adoptModel(model models.Model, directory string) bool {
	backend := backendFor(s.backend.Selected)
	if model.Role != models.Accurate || model.Backend.Normalise() != backend.Normalise() {
		return false
	}
	// Deliberately not modelProblem: that stays silent about a directory which
	// is not there yet, because configuring before downloading is a normal
	// order to work in — and "not there yet" is exactly the state this exists
	// for. The field is left alone only when it already names something this
	// backend can really read.
	if current := strings.TrimSpace(s.modelPath.Text); current != "" {
		if _, err := os.Stat(filepath.Join(current, backend.Weights())); err == nil {
			return false
		}
	}
	s.modelPath.SetText(filepath.Join(directory, model.Name))
	return true
}

func (s *settingsTab) sayModels(text string) {
	s.modelsStatus.SetText(text)
	if text == "" {
		s.modelsStatus.Hide()
		return
	}
	s.modelsStatus.Show()
}

// modelsNeeded reports which of the chosen backend's models are missing, for
// the test that keeps them above the fold and for anything else that wants to
// ask.
func (s *settingsTab) modelsNeeded() []models.State {
	var missing []models.State
	for _, state := range models.Scan(s.modelsDir()) {
		if state.Installed {
			continue
		}
		if state.Model.Backend.Normalise() == backendFor(s.backend.Selected).Normalise() &&
			state.Model.Role == models.Accurate {
			missing = append(missing, state)
		}
	}
	return missing
}

var _ = localserver.BackendCPU // the tab talks about backends through backendFor
