package session

import (
	"testing"

	"plexo/internal/model"
)

// TestCatalogRoomTitleDecoded: fserv stores room titles HTML-escaped (CCR input
// goes through escapeHTML) and ORS sends them verbatim, so the core must decode
// once before publishing the catalog. Without this the join dialog and command
// palette render "Rock &amp; Roll" literally.
func TestCatalogRoomTitleDecoded(t *testing.T) {
	var got []model.PublicRoom
	s := New(Config{
		Character: "Vix",
		OnCatalog: func(_ string, _ []model.OfficialChannel, rooms []model.PublicRoom) {
			got = rooms
		},
	})
	if err := s.handle(jsonFrame("ORS", `{"channels":[{"name":"ADH-AbC123","title":"Rock &amp; Roll","characters":3}]}`)); err != nil {
		t.Fatalf("ORS: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Rock & Roll" {
		t.Fatalf("catalog = %+v, want one room titled %q", got, "Rock & Roll")
	}
}

// TestRoomTitleDecoded: a self JCH for a private room carries the stored,
// escaped title too; it must be decoded before it becomes the conversation
// title (sidebar, pane, room admin).
func TestRoomTitleDecoded(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("JCH", `{"channel":"ADH-AbC123","title":"Rock &amp; Roll","character":{"identity":"Vix"}}`)); err != nil {
		t.Fatalf("JCH: %v", err)
	}
	cs := s.st.convs[convKey(model.ConvRef{Kind: model.ConvRoom, ID: "ADH-AbC123"})]
	if cs == nil || cs.title != "Rock & Roll" {
		t.Fatalf("room title = %q, want %q", cs.title, "Rock & Roll")
	}
}

// TestInviteTitleDecoded: CIU titles are wire-escaped like every room title and
// are decoded before the invitation is published.
func TestInviteTitleDecoded(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("CIU", `{"sender":"Kira","title":"Rock &amp; Roll","name":"ADH-secret"}`)); err != nil {
		t.Fatalf("CIU: %v", err)
	}
	inv := s.st.invites[convKey(model.ConvRef{Kind: model.ConvRoom, ID: "ADH-secret"})]
	if inv.Title != "Rock & Roll" {
		t.Fatalf("invite title = %q, want %q", inv.Title, "Rock & Roll")
	}
}
