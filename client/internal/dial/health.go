package dial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
	"github.com/dennis2lee/local-dictation/client/internal/transport"
)

// Health is what a connection test found.
type Health struct {
	Language protocol.Language
	OK       bool
	// Detail is shown next to the LED. Either a summary of the server or the
	// reason it could not be reached.
	Detail string
	Model  string
	// DraftModel is empty when the server runs a single model.
	DraftModel string
	Version    string
}

// TestConnection is what the Settings tab's "Test connections" button calls.
//
// It checks readiness over HTTP rather than opening a dictation session,
// because a session would take a capacity slot from someone actually dictating
// and would say nothing extra about whether the server is healthy.
func (d *Dialer) TestConnection(ctx context.Context, language protocol.Language) Health {
	settings := d.Settings()
	result := Health{Language: language}

	if settings.Mode == config.ModeLocal {
		server, running := d.local.Running(language)
		if !running {
			result.Detail = "Not started yet. It starts the first time you dictate."
			return result
		}
		return probe(ctx, fmt.Sprintf("http://127.0.0.1:%d/health/ready", server.Port()), nil, language)
	}

	tlsConfig, err := transport.BuildTLSConfig(
		settings.Remote.TLS.Enabled,
		settings.Remote.TLS.CACertificate,
		settings.Remote.TLS.ClientCertificate,
		settings.Remote.TLS.ClientKey,
		settings.Remote.TLS.InsecureSkipVerify,
	)
	if err != nil {
		result.Detail = err.Error()
		return result
	}

	port := settings.PortFor(language)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
	}
	return probe(ctx, settings.HealthURLFor(language, port), client, language)
}

func probe(ctx context.Context, url string, client *http.Client, language protocol.Language) Health {
	result := Health{Language: language}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		result.Detail = friendly(err)
		return result
	}
	defer response.Body.Close()

	var body struct {
		Status     string `json:"status"`
		Language   string `json:"language"`
		Model      string `json:"model"`
		DraftModel string `json:"draft_model"`
		Version    string `json:"version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		result.Detail = fmt.Sprintf("HTTP %d, but the response was not a Local Dictation server",
			response.StatusCode)
		return result
	}

	result.Model, result.DraftModel, result.Version = body.Model, body.DraftModel, body.Version

	switch {
	case body.Language != string(language):
		// The single most confusing misconfiguration: the server answers, it
		// looks healthy, and it silently transcribes the wrong language.
		result.Detail = fmt.Sprintf("This port serves %s, not %s. Check the ports.",
			protocol.Language(body.Language), language)
	case response.StatusCode == http.StatusServiceUnavailable:
		result.Detail = "Reachable, still loading the model."
	case response.StatusCode != http.StatusOK:
		result.Detail = fmt.Sprintf("HTTP %d", response.StatusCode)
	case body.Status != "ready":
		result.Detail = "Reachable, not ready yet."
	default:
		result.OK = true
		result.Detail = fmt.Sprintf("Ready — %s", describeModels(body.Model, body.DraftModel))
	}
	return result
}

func describeModels(model, draft string) string {
	if draft == "" {
		return model
	}
	return fmt.Sprintf("%s, drafting with %s", model, draft)
}

func friendly(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		return "Nothing is listening on that port."
	case strings.Contains(message, "no such host"):
		return "That address does not resolve."
	case strings.Contains(message, "certificate"):
		return "The server certificate was not trusted. Set the CA certificate in Settings."
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded"):
		return "Timed out. Check the address and the firewall."
	default:
		return err.Error()
	}
}
