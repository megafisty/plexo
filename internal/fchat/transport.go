package fchat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// DefaultChatURL is the production F-Chat WebSocket endpoint.
const DefaultChatURL = "wss://chat.f-list.net/chat2"

// DefaultMaxMessageBytes bounds a single inbound message. The server's
// priv_max is 50000 bytes, and the JSON envelope adds overhead, so allow
// generous headroom while still bounding memory.
const DefaultMaxMessageBytes = 1 << 20 // 1 MiB

// ErrProtocol indicates a WebSocket-level violation: an unexpected message
// type, invalid UTF-8, or an oversized message. Per the F-Chat advisories this
// is a non-retryable condition and must not auto-reconnect.
var ErrProtocol = errors.New("fchat: websocket protocol error")

// HandshakeError reports a failed WebSocket upgrade before the F-Chat protocol
// started. StatusCode is non-zero when the server answered the HTTP request.
type HandshakeError struct {
	StatusCode int
	Err        error
}

func (e *HandshakeError) Error() string {
	if e.StatusCode == 0 {
		return "fchat: websocket handshake: " + e.Err.Error()
	}
	return fmt.Sprintf("fchat: websocket handshake: HTTP %d: %v", e.StatusCode, e.Err)
}

func (e *HandshakeError) Unwrap() error { return e.Err }

// DialConfig configures the WebSocket transport to an F-Chat server.
type DialConfig struct {
	// URL is the F-Chat endpoint; empty uses DefaultChatURL.
	URL string

	// HTTPClient performs the upgrade. Tests inject httptest's TLS client;
	// production leaves it nil to use the default client.
	HTTPClient *http.Client

	// Origin, when set, is sent as the Origin header.
	Origin string

	// UserAgent, when set, is sent as the User-Agent header.
	UserAgent string

	// MaxMessageBytes bounds a single inbound message. Zero uses
	// DefaultMaxMessageBytes.
	MaxMessageBytes int64
}

// Dialer adapts a DialConfig into a dial function, matching session.Dialer.
func (cfg DialConfig) Dialer() func(ctx context.Context) (Conn, error) {
	return func(ctx context.Context) (Conn, error) { return Dial(ctx, cfg) }
}

// Dial opens a WebSocket connection to an F-Chat server and returns it as a
// Conn. Each WebSocket text message carries one "XXX {json}" command; control
// frames are handled by the transport.
func Dial(ctx context.Context, cfg DialConfig) (Conn, error) {
	url := cfg.URL
	if url == "" {
		url = DefaultChatURL
	}
	limit := cfg.MaxMessageBytes
	if limit == 0 {
		limit = DefaultMaxMessageBytes
	}

	opts := &websocket.DialOptions{
		HTTPClient: cfg.HTTPClient,
		// Offer permessage-deflate; if the server does not negotiate it the
		// connection stays uncompressed. F-Chat traffic is repetitive text on a
		// long-lived socket, so reuse the sliding window across messages.
		CompressionMode: websocket.CompressionContextTakeover,
	}
	if cfg.Origin != "" || cfg.UserAgent != "" {
		opts.HTTPHeader = http.Header{}
		if cfg.Origin != "" {
			opts.HTTPHeader.Set("Origin", cfg.Origin)
		}
		if cfg.UserAgent != "" {
			opts.HTTPHeader.Set("User-Agent", cfg.UserAgent)
		}
	}

	ws, resp, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		he := &HandshakeError{Err: err}
		if resp != nil {
			he.StatusCode = resp.StatusCode
		}
		return nil, he
	}
	ws.SetReadLimit(limit)

	return &wsConn{ws: ws}, nil
}

// wsConn adapts a coder/websocket connection to the F-Chat Conn interface.
// It is safe for concurrent Write calls; Read must be called serially.
type wsConn struct {
	ws *websocket.Conn
}

func (c *wsConn) Read(ctx context.Context) (Frame, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return Frame{}, c.mapReadError(err)
	}
	if typ != websocket.MessageText {
		return Frame{}, fmt.Errorf("%w: unexpected message type %d", ErrProtocol, typ)
	}
	frame, err := Parse(data)
	if err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func (c *wsConn) Write(ctx context.Context, cmd Frame) error {
	return c.ws.Write(ctx, websocket.MessageText, cmd.Marshal())
}

func (c *wsConn) Close() error {
	// F-Chat does not follow the WebSocket close handshake, and a blocking
	// close handshake would stall the session actor. Just drop the socket.
	_ = c.ws.CloseNow()
	return nil
}

// mapReadError translates WebSocket close/read errors into the F-Chat
// taxonomy. A clean close is a normal disconnect; a protocol violation is not
// retryable.
func (c *wsConn) mapReadError(err error) error {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.StatusProtocolError,
			websocket.StatusUnsupportedData,
			websocket.StatusInvalidFramePayloadData,
			websocket.StatusMessageTooBig:
			return fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		return ErrClosed{}
	}
	if errors.Is(err, websocket.ErrMessageTooBig) {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	var ne net.Error
	if errors.Is(err, net.ErrClosed) || errors.As(err, &ne) {
		return ErrClosed{}
	}
	return err
}
