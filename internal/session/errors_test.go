package session

import (
	"errors"
	"fmt"
	"testing"

	"plexo/internal/fchat"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason string
		sv     string
		auto   bool
	}{
		{"network", errors.New("dial tcp: refused"), "network", "normal", true},
		{"handshake", &fchat.HandshakeError{StatusCode: 502, Err: errors.New("bad gateway")}, "network", "normal", true},
		{"malformed", fmt.Errorf("wrap: %w", fchat.ErrMalformed), "protocol_mismatch", "severe", false},
		{"protocol", fmt.Errorf("wrap: %w", fchat.ErrProtocol), "protocol_mismatch", "severe", false},
		{"invalidCredentials", fmt.Errorf("wrap: %w", fchat.ErrInvalidCredentials), "auth_failed", "severe", false},
		{"noCredentials", fchat.ErrNoCredentials, "auth_failed", "severe", false},
		{"protocolError", &protocolError{"IDN: bad type"}, "protocol_mismatch", "severe", false},
		{"disconnected", &disconnectedError{reason: "taken_over", severity: "severe", auto: false}, "taken_over", "severe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, sv, auto := classify(tc.err)
			if reason != tc.reason || sv != tc.sv || auto != tc.auto {
				t.Fatalf("classify = (%q, %q, %v), want (%q, %q, %v)", reason, sv, auto, tc.reason, tc.sv, tc.auto)
			}
		})
	}
}

// TestFatalERRLoginCodes: fserv sends generic messages for login failures, so
// IDENT_FAILED (4) and NO_LOGIN_SLOT (62) must be classified from the numeric
// code alone.
func TestFatalERRLoginCodes(t *testing.T) {
	cases := []struct {
		code    int
		reason  string
		refresh bool
	}{
		{4, "ident_failed", true},
		{62, "no_login_slots", false},
	}
	for _, tc := range cases {
		de := fatalERR(fchat.EREvent{Number: tc.code, Message: "generic server text"})
		if de == nil {
			t.Fatalf("fatalERR(%d) = nil, want %s", tc.code, tc.reason)
		}
		if de.reason != tc.reason {
			t.Fatalf("fatalERR(%d).reason = %q, want %q", tc.code, de.reason, tc.reason)
		}
		if de.auto {
			t.Fatalf("fatalERR(%d).auto = true, want false", tc.code)
		}
		if de.refreshAuth != tc.refresh {
			t.Fatalf("fatalERR(%d).refreshAuth = %v, want %v", tc.code, de.refreshAuth, tc.refresh)
		}
	}
}
