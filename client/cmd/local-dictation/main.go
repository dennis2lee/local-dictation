// Command local-dictation is the desktop client.
//
// It runs as a windowed application with a tray icon. The two non-GUI flags
// exist for the installers and for support: --check reports whether this
// machine can actually dictate, and --version is what the update manifest is
// compared against.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/audio"
	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/localserver"
	"github.com/dennis2lee/local-dictation/client/internal/platform"
	"github.com/dennis2lee/local-dictation/client/internal/singleton"
	"github.com/dennis2lee/local-dictation/client/internal/ui"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=0.2.0"
var version = "0.1.28"

func main() {
	var (
		configPath  = flag.String("config", "", "path to settings.json (default: the per-user location)")
		showVersion = flag.Bool("version", false, "print the version and exit")
		check       = flag.Bool("check", false, "report whether this machine can dictate, and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	settings, path, err := loadSettings(*configPath)
	if err != nil {
		fail(err)
	}

	stateDir := filepath.Dir(path)

	if *check {
		os.Exit(runCheck(settings, path))
	}

	// One copy per user. The app lives in the tray, so launching it again —
	// from the Start menu, from a shortcut, from double-clicking the thing
	// already running — used to add a second tray icon, register the same
	// global shortcut a second time and start a second speech server. None of
	// that announces itself; it just behaves oddly later.
	//
	// Held for the life of the process. The second copy asks the first to show
	// itself and exits, which is what launching an app that is already running
	// was meant to do.
	singleton.Dir(stateDir)
	instance, err := singleton.Acquire(ui.AppID)
	if errors.Is(err, singleton.ErrAlreadyRunning) {
		return
	}
	if err != nil {
		// Not fatal. Failing to take the lock is not a reason to refuse to
		// run — a machine where this cannot work should still dictate.
		fmt.Fprintf(os.Stderr, "local-dictation: %v\n", err)
	}
	defer instance.Release()

	// From here on there may be nowhere for a failure to appear. The Windows
	// build is linked with -H=windowsgui and has no console at all; a macOS
	// bundle opened from Finder has no terminal either. A crash on the way to
	// the first window is then indistinguishable from nothing happening, which
	// is precisely how this app shipped eight releases unable to start.
	diagnostics := openDiagnosticLog(stateDir)
	if diagnostics != nil {
		defer diagnostics.Close()
		defer func() {
			if problem := recover(); problem != nil {
				fmt.Fprintf(diagnostics, "%s panic: %v\n\n%s\n",
					time.Now().Format(time.RFC3339), problem, debug.Stack())
				_ = diagnostics.Sync()
				panic(problem) // still crash, and still say so on a console if there is one
			}
		}()
		fmt.Fprintf(diagnostics, "%s starting %s\n", time.Now().Format(time.RFC3339), version)
	}

	application, err := ui.New(ui.Options{
		Version:  version,
		Settings: settings,
		StateDir: stateDir,
		Show:     instance.Show(),
	})
	if err != nil {
		reportStartupFailure(diagnostics, err)
		fail(err)
	}
	defer application.Quit()
	application.Run()
}

// openDiagnosticLog returns the file startup problems are recorded in, or nil
// if it cannot be opened — in which case the app still runs. It is truncated
// each launch: this answers "why did nothing happen just now", not "what has
// this machine been doing".
func openDiagnosticLog(stateDir string) *os.File {
	if stateDir == "" {
		return nil
	}
	file, err := os.Create(filepath.Join(stateDir, "startup.log"))
	if err != nil {
		return nil
	}
	return file
}

func reportStartupFailure(diagnostics *os.File, err error) {
	if diagnostics == nil {
		return
	}
	fmt.Fprintf(diagnostics, "%s could not start: %v\n", time.Now().Format(time.RFC3339), err)
	_ = diagnostics.Sync()
}

func loadSettings(configPath string) (config.Config, string, error) {
	if configPath != "" {
		settings, err := config.LoadFrom(configPath)
		return settings, configPath, err
	}

	path, err := config.Path()
	if err != nil {
		return config.Default(), "", err
	}
	settings, err := config.Load()
	if err != nil {
		return config.Default(), path, err
	}

	// Write the defaults out on first run so there is a file to point support
	// at, and so the user can see what the settings even are.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := settings.SaveTo(path); err != nil {
			return settings, path, fmt.Errorf("create %s: %w", path, err)
		}
	}
	return settings, path, nil
}

// runCheck is what an installer or a support request runs. It never opens a
// window and never starts a session; it reports each prerequisite separately so
// the failing one is obvious.
func runCheck(settings config.Config, settingsPath string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	problems, warnings := 0, 0
	report := func(label string, ok bool, detail string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			problems++
		}
		fmt.Printf("%s  %-22s %s\n", mark, label, detail)
	}
	// Separate from a failure because it is a different thing to tell someone:
	// dictation will run, and it will be worse. Counting it as a problem would
	// make the closing line — "before dictation will work" — untrue, and an
	// installer that gates on the exit code would refuse a usable install.
	warn := func(label, detail string) {
		warnings++
		fmt.Printf("warn  %-22s %s\n", label, detail)
	}

	fmt.Printf("Local Dictation %s\n", version)
	// The real path, not the default one: a diagnostic that reports a file it
	// did not read is worse than no diagnostic.
	fmt.Printf("settings: %s\n", settingsPath)
	// Worth printing even when everything passes: it is the file to ask for
	// when someone reports that the app does nothing at all.
	fmt.Printf("startup log: %s\n\n", filepath.Join(filepath.Dir(settingsPath), "startup.log"))

	if err := settings.Validate(); err != nil {
		report("settings", false, err.Error())
	} else {
		report("settings", true, fmt.Sprintf("mode=%s language=%s shortcut=%s",
			settings.Mode, settings.Language, settings.Hotkey))
	}

	if ok, reason := platform.Available(); ok {
		report("text at the cursor", true, "permitted")
	} else {
		report("text at the cursor", false, reason)
	}

	capture, err := audio.NewCapture()
	if err != nil {
		report("microphone", false, err.Error())
	} else {
		devices, err := capture.Devices()
		if err != nil {
			report("microphone", false, err.Error())
		} else {
			report("microphone", true, fmt.Sprintf("%d device(s) available", len(devices)))
		}
		_ = capture.Close()
	}

	if settings.Mode == config.ModeLocal {
		checkLocalServer(ctx, settings, report, warn)
	} else {
		report("server mode", true, fmt.Sprintf("remote: %s (ko %d, en %d)",
			settings.Remote.Host, settings.Remote.KoreanPort, settings.Remote.EnglishPort))
	}

	fmt.Println()
	if problems == 0 && warnings == 0 {
		fmt.Println("Everything needed to dictate is in place.")
		return 0
	}
	if problems == 0 {
		fmt.Printf("Dictation will work. %d thing(s) above will make it worse than it needs to be.\n", warnings)
		return 0
	}
	fmt.Printf("%d problem(s) need attention before dictation will work.\n", problems)
	return 1
}

func checkLocalServer(
	ctx context.Context,
	settings config.Config,
	report func(string, bool, string),
	warn func(string, string),
) {
	serverDir, err := localserver.ResolveServerDir(settings.Local.ServerDir)
	if err != nil {
		report("server files", false, err.Error())
		return
	}
	report("server files", true, serverDir)

	// Each backend reads a different conversion of the model, so this has to
	// be known before the model is checked at all: holding an OpenVINO export
	// to model.bin would fail a perfectly good install.
	backend := settings.Local.Backend.Normalise()
	if !backend.Supported() {
		report("decoder", false, fmt.Sprintf(
			"%s needs hardware this operating system does not have", backend.Label()))
	} else {
		report("decoder", true, fmt.Sprintf("%s (%s, reads %s)",
			backend.Label(), backend.Engine(), backend.Weights()))
	}

	interpreter, err := localserver.FindPython(ctx, settings.Local.PythonPath, config.DefaultPythonCandidates())
	if err != nil {
		report("python", false, err.Error())
		return
	}
	report("python", true, fmt.Sprintf("%s (%s)", interpreter.Path, interpreter.Version))

	if settings.Local.ModelPath == "" {
		report("model", false, "no model directory configured; see docs/model-setup.md")
	} else if info, err := os.Stat(settings.Local.ModelPath); err != nil || !info.IsDir() {
		report("model", false, fmt.Sprintf("%s is not readable; see docs/model-setup.md", settings.Local.ModelPath))
	} else if _, err := os.Stat(filepath.Join(settings.Local.ModelPath, backend.Weights())); err != nil {
		report("model", false, fmt.Sprintf("%s has no %s, which %s reads; see docs/model-setup.md",
			settings.Local.ModelPath, backend.Weights(), backend.Label()))
	} else {
		report("model", true, settings.Local.ModelPath)
	}

	// Not a failure: without it the server still starts and still transcribes.
	// It transcribes sounds that are not speech as well, though, because the
	// detector it falls back to cannot tell them apart — and Whisper's answer
	// to a window of breath is a sentence nobody said.
	if detector := localserver.ResolveDetector(settings.Local.VadModelPath, settings.Local.ModelPath); detector != "" {
		report("voice detector", true, detector)
	} else {
		warn("voice detector", fmt.Sprintf(
			"no %s configured or beside the model. Without it the server "+
				"compares loudness instead, which reads a breath as speech, and "+
				"the decoder answers that with text nobody said — usually "+
				"\"감사합니다\". Install it from Settings › Models.",
			localserver.DetectorName))
	}

	// The draft model runs on the same backend as the accurate one, so it has
	// to be the same format. A CTranslate2 draft beside an OpenVINO model is a
	// server that will not start.
	if draft := settings.Local.DraftModelPath; draft != "" {
		if _, err := os.Stat(filepath.Join(draft, backend.Weights())); err != nil {
			report("draft model", false, fmt.Sprintf("%s has no %s; see docs/latency.md",
				draft, backend.Weights()))
		} else {
			report("draft model", true, draft)
		}
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "local-dictation: %v\n", err)
	os.Exit(1)
}
