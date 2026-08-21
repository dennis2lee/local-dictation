// Command local-dictation is the desktop client.
//
// It runs as a windowed application with a tray icon. The two non-GUI flags
// exist for the installers and for support: --check reports whether this
// machine can actually dictate, and --version is what the update manifest is
// compared against.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/audio"
	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/localserver"
	"github.com/dennis2lee/local-dictation/client/internal/platform"
	"github.com/dennis2lee/local-dictation/client/internal/ui"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=0.2.0"
var version = "0.1.0"

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
		os.Exit(runCheck(settings, stateDir))
	}

	application, err := ui.New(ui.Options{
		Version:  version,
		Settings: settings,
		StateDir: stateDir,
	})
	if err != nil {
		fail(err)
	}
	defer application.Quit()
	application.Run()
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
func runCheck(settings config.Config, stateDir string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	problems := 0
	report := func(label string, ok bool, detail string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			problems++
		}
		fmt.Printf("%s  %-22s %s\n", mark, label, detail)
	}

	fmt.Printf("Local Dictation %s\n", version)
	fmt.Printf("settings: %s\n\n", filepath.Join(stateDir, "settings.json"))

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
		checkLocalServer(ctx, settings, report)
	} else {
		report("server mode", true, fmt.Sprintf("remote: %s (ko %d, en %d)",
			settings.Remote.Host, settings.Remote.KoreanPort, settings.Remote.EnglishPort))
	}

	fmt.Println()
	if problems == 0 {
		fmt.Println("Everything needed to dictate is in place.")
		return 0
	}
	fmt.Printf("%d problem(s) need attention before dictation will work.\n", problems)
	return 1
}

func checkLocalServer(ctx context.Context, settings config.Config, report func(string, bool, string)) {
	serverDir, err := localserver.ResolveServerDir(settings.Local.ServerDir)
	if err != nil {
		report("server files", false, err.Error())
		return
	}
	report("server files", true, serverDir)

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
	} else if _, err := os.Stat(filepath.Join(settings.Local.ModelPath, "model.bin")); err != nil {
		report("model", false, fmt.Sprintf("%s has no model.bin; see docs/model-setup.md", settings.Local.ModelPath))
	} else {
		report("model", true, settings.Local.ModelPath)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "local-dictation: %v\n", err)
	os.Exit(1)
}
