// Package dial turns "the user picked Korean" into a live server connection.
//
// It is the one place that knows the difference between the two deployment
// modes. In remote mode it builds a URL and dials it. In local mode it makes
// sure a server is running on this machine first, then dials loopback. Above
// this package, nothing else has to care which happened.
package dial

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/localserver"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
	"github.com/dennis2lee/local-dictation/client/internal/session"
	"github.com/dennis2lee/local-dictation/client/internal/transport"
)

// Dialer implements session.Dialer for both modes.
type Dialer struct {
	mu       sync.RWMutex
	settings config.Config
	local    *localserver.Manager
	stateDir string
}

// New builds a dialer for the given settings.
func New(settings config.Config, stateDir string) *Dialer {
	dialer := &Dialer{settings: settings, stateDir: stateDir}
	dialer.local = localserver.NewManager(managerSettings(settings, stateDir))
	return dialer
}

// Settings reports the configuration in force.
func (d *Dialer) Settings() config.Config {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.settings
}

// Update applies new settings, stopping any local server whose configuration
// changed so the next session picks the new one up.
func (d *Dialer) Update(ctx context.Context, settings config.Config) error {
	d.mu.Lock()
	d.settings = settings
	d.mu.Unlock()
	return d.local.Update(ctx, managerSettings(settings, d.stateDir))
}

// LocalManager exposes the supervisor, for the Settings tab's status display.
func (d *Dialer) LocalManager() *localserver.Manager { return d.local }

// Dial opens a session for a language.
func (d *Dialer) Dial(ctx context.Context, language protocol.Language, progress func(string)) (session.Session, error) {
	settings := d.Settings()

	port := settings.PortFor(language)
	if settings.Mode == config.ModeLocal {
		server, err := d.local.Ensure(ctx, language, progress)
		if err != nil {
			return nil, err
		}
		port = server.Port()
	}

	tlsConfig, err := transport.BuildTLSConfig(
		settings.Mode == config.ModeRemote && settings.Remote.TLS.Enabled,
		settings.Remote.TLS.CACertificate,
		settings.Remote.TLS.ClientCertificate,
		settings.Remote.TLS.ClientKey,
		settings.Remote.TLS.InsecureSkipVerify,
	)
	if err != nil {
		return nil, err
	}

	endpoint := settings.EndpointFor(language, port)
	if progress != nil {
		progress(fmt.Sprintf("Connecting to %s…", endpoint))
	}

	return transport.Dial(ctx, transport.DialOptions{
		Endpoint:  endpoint,
		TLSConfig: tlsConfig,
		Timeout:   10 * time.Second,
	})
}

// Shutdown stops anything this dialer started.
func (d *Dialer) Shutdown(ctx context.Context) error { return d.local.StopAll(ctx) }

func managerSettings(settings config.Config, stateDir string) localserver.ManagerSettings {
	return localserver.ManagerSettings{
		PythonPath:     settings.Local.PythonPath,
		ServerDir:      settings.Local.ServerDir,
		ModelPath:      settings.Local.ModelPath,
		DraftModelPath: settings.Local.DraftModelPath,
		VadModelPath:   settings.Local.VadModelPath,
		StateDir:       stateDir,
		CPUThreads:     settings.Local.CPUThreads,
		MinSpeechMs:    settings.Local.MinSpeechMs,
		Backend:        settings.Local.Backend,
		KoreanPort:     settings.Local.KoreanPort,
		EnglishPort:    settings.Local.EnglishPort,
	}
}
