// Package fchat implements everything that speaks F-List: the F-Chat wire
// protocol (framing, connection abstraction, typed command payloads) plus the
// REST adapters for API tickets and character field mapping. Nothing else in
// the tree names F-List endpoints or payloads.
package fchat

import (
	"bytes"
	"errors"
	"fmt"
)

// ErrMalformed indicates a protocol frame that could not be parsed. Per the
// F-Chat protocol advisories, malformed input is a strong signal to disconnect.
var ErrMalformed = errors.New("fchat: malformed command")

// Frame is a single protocol frame: a three-character uppercase code and an
// optional JSON payload. Frames without a payload carry no trailing space.
type Frame struct {
	Code string
	Data []byte
}

// Parse decodes a raw frame of the form "XXX {json}" or "XXX".
func Parse(raw []byte) (Frame, error) {
	raw = bytes.TrimRight(raw, "\r\n")
	if len(raw) < 3 {
		return Frame{}, fmt.Errorf("%w: too short (%d bytes)", ErrMalformed, len(raw))
	}

	i := bytes.IndexByte(raw, ' ')
	code := raw
	var data []byte
	if i >= 0 {
		code = raw[:i]
		data = raw[i+1:]
	}

	if len(code) != 3 {
		return Frame{}, fmt.Errorf("%w: bad code %q", ErrMalformed, string(code))
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return Frame{}, fmt.Errorf("%w: bad code %q", ErrMalformed, string(code))
		}
	}
	if len(data) == 0 {
		data = nil
	}
	return Frame{Code: string(code), Data: data}, nil
}

// Marshal encodes a command as a wire frame.
func (c Frame) Marshal() []byte {
	if len(c.Data) == 0 {
		return []byte(c.Code)
	}
	out := make([]byte, 0, len(c.Code)+1+len(c.Data))
	out = append(out, c.Code...)
	out = append(out, ' ')
	return append(out, c.Data...)
}

// String renders the frame for logs.
func (c Frame) String() string { return string(c.Marshal()) }
