// Package transport carries the protocol over a WebSocket.
//
// It owns exactly one concern: framing and connection lifetime. It knows
// nothing about audio devices, cursors or UI state, which is what lets the
// session controller be tested against a fake transport and this package be
// tested against a real server.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// ReadLimit caps a single inbound frame. Transcripts are small; anything near
// this is a server bug or something that is not our server.
const ReadLimit = 1 << 20

// DialOptions describes one connection attempt.
type DialOptions struct {
	Endpoint  string
	TLSConfig *tls.Config
	// Timeout bounds the handshake only, not the session.
	Timeout time.Duration
}

// Conn is a live dictation session socket.
type Conn struct {
	conn   *websocket.Conn
	events chan protocol.ServerEvent

	writeMu sync.Mutex

	closeOnce sync.Once
	closeErr  error

	readErrMu sync.Mutex
	readErr   error
}

// Dial opens a session socket and starts reading from it.
func Dial(ctx context.Context, options DialOptions) (*Conn, error) {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:     options.TLSConfig,
			TLSHandshakeTimeout: timeout,
			DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
		},
	}

	conn, response, err := websocket.Dial(dialCtx, options.Endpoint, &websocket.DialOptions{
		HTTPClient: httpClient,
	})
	if err != nil {
		return nil, describeDialError(options.Endpoint, response, err)
	}
	conn.SetReadLimit(ReadLimit)

	client := &Conn{conn: conn, events: make(chan protocol.ServerEvent, 32)}
	go client.readLoop()
	return client, nil
}

// Events yields decoded server events until the connection ends, at which point
// the channel closes. Check Err afterwards to find out why.
func (c *Conn) Events() <-chan protocol.ServerEvent { return c.events }

// Err reports why the read loop stopped, or nil for a clean close.
func (c *Conn) Err() error {
	c.readErrMu.Lock()
	defer c.readErrMu.Unlock()
	return c.readErr
}

func (c *Conn) readLoop() {
	defer close(c.events)
	for {
		messageType, payload, err := c.conn.Read(context.Background())
		if err != nil {
			if !isNormalClosure(err) {
				c.setReadErr(err)
			}
			return
		}
		if messageType != websocket.MessageText {
			continue // the server has nothing binary to say in v1
		}
		event, err := protocol.DecodeServerEvent(payload)
		if err != nil {
			if errors.Is(err, protocol.ErrUnknownEvent) {
				// Forward compatibility: a newer server may add events. Ignoring
				// one is better than dropping the user's session mid-sentence.
				continue
			}
			c.setReadErr(err)
			return
		}
		c.events <- event
	}
}

func (c *Conn) setReadErr(err error) {
	c.readErrMu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.readErrMu.Unlock()
}

// SendStart opens the session. It must be the first thing written.
func (c *Conn) SendStart(ctx context.Context, start protocol.Start) error {
	return c.writeJSON(ctx, start)
}

// SendAudio writes one PCM frame.
func (c *Conn) SendAudio(ctx context.Context, pcm []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, websocket.MessageBinary, pcm)
}

// SendFlush asks for the final transcript.
func (c *Conn) SendFlush(ctx context.Context) error {
	return c.writeJSON(ctx, protocol.NewFlush())
}

// SendStop ends the session.
func (c *Conn) SendStop(ctx context.Context) error {
	return c.writeJSON(ctx, protocol.NewStop())
}

func (c *Conn) writeJSON(ctx context.Context, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// The mutex matters: audio frames are written from the capture callback
	// while control messages come from the controller, and coder/websocket
	// does not allow concurrent writers.
	return wsjson(ctx, c.conn, value)
}

// Close ends the session politely, then tears the socket down.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.conn.Close(websocket.StatusNormalClosure, "")
		if c.closeErr != nil {
			c.conn.CloseNow()
		}
	})
	return c.closeErr
}

// CloseNow drops the socket without a closing handshake, for teardown paths
// where nobody is waiting.
func (c *Conn) CloseNow() {
	c.closeOnce.Do(func() { c.closeErr = c.conn.CloseNow() })
}

func isNormalClosure(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway
}

// describeDialError turns a connection failure into something a user can act on.
func describeDialError(endpoint string, response *http.Response, err error) error {
	if response != nil && response.StatusCode != 0 && response.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("connect to %s: server answered HTTP %d (is that the dictation port?)",
			endpoint, response.StatusCode)
	}

	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return fmt.Errorf("connect to %s: the server certificate was not trusted — "+
			"point Settings at your internal CA certificate: %w", endpoint, err)
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("connect to %s: timed out — check the address and that the server is running", endpoint)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("connect to %s: timed out", endpoint)
	}
	return fmt.Errorf("connect to %s: %w", endpoint, err)
}

// BuildTLSConfig turns the client's TLS settings into a *tls.Config.
// Returns nil when TLS is off, which is the loopback case.
func BuildTLSConfig(enabled bool, caCertificate, clientCertificate, clientKey string, insecure bool) (*tls.Config, error) {
	if !enabled {
		return nil, nil
	}

	config := &tls.Config{MinVersion: tls.VersionTLS12}

	if insecure {
		// Bring-up escape hatch. The UI shows it as a warning, not a setting.
		config.InsecureSkipVerify = true
	}

	if caCertificate != "" {
		pem, err := os.ReadFile(caCertificate)
		if err != nil {
			return nil, fmt.Errorf("read the CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s contains no PEM certificates", caCertificate)
		}
		config.RootCAs = pool
	}

	if clientCertificate != "" || clientKey != "" {
		if clientCertificate == "" || clientKey == "" {
			return nil, errors.New("a client certificate needs both the certificate and the key")
		}
		pair, err := tls.LoadX509KeyPair(clientCertificate, clientKey)
		if err != nil {
			return nil, fmt.Errorf("load the client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{pair}
	}

	return config, nil
}
