package session

import (
	"strings"
	"testing"

	"plexo/internal/model"
)

// TestSendMessageLengthLimits: DMs use priv_max and channel messages chat_max,
// matching fserv. A sent frame cannot reach a socket in this unit test, so a
// body within the limit proceeds to the "not connected" failure while an
// over-long one is rejected up front with too_long.
func TestSendMessageLengthLimits(t *testing.T) {
	s := New(Config{Character: "Vix"})
	s.st.vars.ChatMax = 4096
	s.st.vars.PrivMax = 50000

	ch := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Neko"}

	if res := s.handleCommand(model.Command{Op: model.OpSendMessage, Conv: ch, Body: strings.Repeat("x", 4097)}); res.ErrorCode != "too_long" {
		t.Fatalf("channel over chat_max: code = %q, want too_long", res.ErrorCode)
	}
	if res := s.handleCommand(model.Command{Op: model.OpSendMessage, Conv: dm, Body: strings.Repeat("x", 50001)}); res.ErrorCode != "too_long" {
		t.Fatalf("DM over priv_max: code = %q, want too_long", res.ErrorCode)
	}
	// A 5000-byte DM is within priv_max even though it exceeds chat_max.
	if res := s.handleCommand(model.Command{Op: model.OpSendMessage, Conv: dm, Body: strings.Repeat("x", 5000)}); res.ErrorCode == "too_long" {
		t.Fatal("DM within priv_max rejected as too_long")
	}
	if res := s.handleCommand(model.Command{Op: model.OpSendMessage, Conv: ch, Body: strings.Repeat("x", 4096)}); res.ErrorCode == "too_long" {
		t.Fatal("channel message at chat_max rejected as too_long")
	}
}

// TestSendLRPLengthLimit: LRP is bounded by lfrp_max, which the session now
// parses from VAR and enforces before handing the frame to the writer.
func TestSendLRPLengthLimit(t *testing.T) {
	s := New(Config{Character: "Vix"})
	s.st.vars.LfrpMax = 50000
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	if res := s.handleCommand(model.Command{Op: model.OpSendLRP, Conv: conv, Body: strings.Repeat("x", 50001)}); res.ErrorCode != "too_long" {
		t.Fatalf("LRP over lfrp_max: code = %q, want too_long", res.ErrorCode)
	}
}
