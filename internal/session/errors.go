package session

import (
	"errors"
	"fmt"
	"strings"

	"plexo/internal/fchat"
)

// disconnectedError carries the surfaced lifecycle result. refreshAuth marks a
// disconnect caused by a rejected ticket: the cached ticket should be
// invalidated so a later connect mints a fresh one.
type disconnectedError struct {
	reason      string
	severity    string
	auto        bool
	refreshAuth bool
	err         error
}

func (e *disconnectedError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("session disconnected: %s (%s): %v", e.reason, e.severity, e.err)
	}
	return fmt.Sprintf("session disconnected: %s (%s)", e.reason, e.severity)
}

func (e *disconnectedError) Unwrap() error { return e.err }

// protocolError indicates a wrong-typed or malformed protocol command. Per the
// F-Chat advisories this must not auto-reconnect.
type protocolError struct{ msg string }

func (e *protocolError) Error() string { return "fchat protocol: " + e.msg }

// decodeFrame decodes an inbound frame's payload, tagging a decode failure as a
// protocolError so the session disconnects for a malformed command. It is the
// single decode entrypoint for inbound handling; fchat.Decode already names the
// command in its error, so callers no longer repeat the code.
func decodeFrame[T any](cmd fchat.Frame) (T, error) {
	v, err := fchat.Decode[T](cmd)
	if err != nil {
		return v, &protocolError{err.Error()}
	}
	return v, nil
}

// classify maps an error to the disconnected taxonomy.
func classify(err error) (reason, severity string, auto bool) {
	var de *disconnectedError
	if errors.As(err, &de) {
		return de.reason, de.severity, de.auto
	}
	var pe *protocolError
	if errors.As(err, &pe) {
		return "protocol_mismatch", "severe", false
	}
	if errors.Is(err, fchat.ErrMalformed) || errors.Is(err, fchat.ErrProtocol) {
		return "protocol_mismatch", "severe", false
	}
	if errors.Is(err, fchat.ErrInvalidCredentials) || errors.Is(err, fchat.ErrNoCredentials) {
		return "auth_failed", "severe", false
	}
	return "network", "normal", true
}

// invalidateTicket drops the cached ticket when the disconnect was caused by a
// rejected one, so a manual reconnect does not replay it.
func (s *Session) invalidateTicket(err error) {
	var de *disconnectedError
	if !errors.As(err, &de) || !de.refreshAuth {
		return
	}
	if inv, ok := s.cfg.Tickets.(fchat.TicketInvalidator); ok {
		inv.Invalidate(s.cfg.Account)
	}
}

// fatalERR maps an ERR command to a session-ending disconnection, or nil when
// the ERR is a per-command failure that does not end the session. Codes listed
// in the protocol advisories must never auto-reconnect; every other ERR is
// surfaced to clients as an error event and the session stays up.
func fatalERR(e fchat.EREvent) *disconnectedError {
	switch e.EffectiveCode() {
	case 2:
		return errState("server_full", "severe", "chat server is full")
	case 4:
		// IDENT_FAILED. fserv sends a generic human-readable message ("Identification
		// failed.") rather than the symbolic name, so the numeric code is the only
		// reliable signal.
		de := errState("ident_failed", "normal", "login rejected; ticket invalid")
		de.refreshAuth = true
		return de
	case 9:
		return errState("banned", "severe", "account is banned from chat")
	case 30:
		return errState("too_many_from_ip", "severe", "too many connections from this address")
	case 31:
		return errState("taken_over", "severe", "character was taken over by another connection")
	case 33:
		return errState("unknown_auth_method", "severe", "unsupported authentication method")
	case 39:
		return errState("timed_out", "severe", "timed out from chat by a moderator")
	case 40:
		return errState("kicked", "severe", "kicked from chat by a moderator")
	case 62:
		return errState("no_login_slots", "normal", "login server is busy")
	}

	// IDN failures may arrive as an ERR with a textual message.
	msg := e.Message
	switch {
	case strings.Contains(msg, "IDENT_FAILED"):
		de := errState("ident_failed", "normal", "login rejected; ticket invalid")
		de.refreshAuth = true
		return de
	case strings.Contains(msg, "TOO_MANY_FROM_IP"):
		return errState("too_many_from_ip", "severe", "too many connections from this address")
	case strings.Contains(msg, "BANNED_FROM_SERVER"):
		return errState("banned", "severe", "account is banned from chat")
	case strings.Contains(msg, "LOGGED_IN_AGAIN"):
		return errState("taken_over", "severe", "character was taken over by another connection")
	case strings.Contains(msg, "UNKNOWN_AUTH_METHOD"):
		return errState("unknown_auth_method", "severe", "unsupported authentication method")
	case strings.Contains(msg, "SERVER_FULL"):
		return errState("server_full", "severe", "chat server is full")
	case strings.Contains(msg, "NO_LOGIN_SLOTS"):
		return errState("no_login_slots", "normal", "login server is busy")
	}
	return nil
}

func errState(reason, severity, msg string) *disconnectedError {
	return &disconnectedError{reason: reason, severity: severity, auto: false, err: errors.New(msg)}
}
