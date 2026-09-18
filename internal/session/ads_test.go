package session

import (
	"strings"
	"testing"

	"plexo/internal/model"
	"plexo/internal/render"
)

func ad(character, msg string) model.Ad {
	return model.Ad{Character: character, Channel: "Looking for RP", Message: msg}
}

func TestAdBufferAddAndList(t *testing.T) {
	b := newAdBuffer(0)
	if !b.add(ad("Alice", "a")) {
		t.Fatal("first add rejected")
	}
	if !b.add(ad("Bob", "b")) {
		t.Fatal("second add rejected")
	}
	got := b.list()
	if len(got) != 2 || got[0].Character != "Alice" || got[1].Character != "Bob" {
		t.Fatalf("list = %+v", got)
	}
	if b.len() != 2 {
		t.Fatalf("len = %d", b.len())
	}
}

func TestAdBufferDuplicateCharacterIgnored(t *testing.T) {
	b := newAdBuffer(0)
	b.add(ad("Alice", "first"))
	if b.add(ad("Alice", "second")) {
		t.Fatal("duplicate add accepted")
	}
	got := b.list()
	if len(got) != 1 || got[0].Message != "first" {
		t.Fatalf("old ad was not kept: %+v", got)
	}
}

func TestAdBufferDuplicateIsCaseInsensitive(t *testing.T) {
	b := newAdBuffer(0)
	b.add(ad("Alice", "a"))
	if b.add(ad("alice", "a")) {
		t.Fatal("case-variant duplicate accepted")
	}
}

func TestAdBufferGet(t *testing.T) {
	b := newAdBuffer(0)
	b.add(ad("Alice", "hello"))
	got, ok := b.get("ALICE")
	if !ok || got.Message != "hello" {
		t.Fatalf("get = %+v, %v", got, ok)
	}
	if _, ok := b.get("Nobody"); ok {
		t.Fatal("unexpected ad for absent character")
	}
}

func TestAdBufferFIFOEviction(t *testing.T) {
	b := newAdBuffer(3)
	for _, name := range []string{"A", "B", "C"} {
		b.add(ad(name, name))
	}
	// Full: adding D evicts A.
	b.add(ad("D", "D"))
	got := b.list()
	if len(got) != 3 || got[0].Character != "B" || got[2].Character != "D" {
		t.Fatalf("after eviction list = %+v", got)
	}
	if _, ok := b.get("A"); ok {
		t.Fatal("A should have been evicted")
	}
	// A may now post again.
	if !b.add(ad("A", "A2")) {
		t.Fatal("re-add after eviction rejected")
	}
}

func TestAdBufferDefaultCapacity(t *testing.T) {
	b := newAdBuffer(0)
	if b.capacity != defaultAdCapacity {
		t.Fatalf("capacity = %d, want %d", b.capacity, defaultAdCapacity)
	}
}

// TestLRPAdRendersBBCode verifies an incoming advertisement is buffered with
// its body rendered, so the client receives display HTML rather than raw
// BBCode and never parses it itself.
func TestLRPAdRendersBBCode(t *testing.T) {
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	s := New(Config{Character: "Vix", Renderer: renderer})
	frame := jsonFrame("LRP", `{"character":"Kira","channel":"Looking for RP","message":"[b]Bold[/b] ad"}`)
	if err := s.handle(frame); err != nil {
		t.Fatal(err)
	}
	ads := s.Ads()
	if len(ads) != 1 {
		t.Fatalf("ads = %+v", ads)
	}
	if !strings.Contains(ads[0].Message, "<b>Bold</b>") {
		t.Fatalf("ad not rendered: %q", ads[0].Message)
	}
	if strings.Contains(ads[0].Message, "[b]") {
		t.Fatalf("ad kept raw BBCode: %q", ads[0].Message)
	}
}

// TestLRPAdResolvesRoomTitle verifies a room advertisement is labelled with the
// room's readable title rather than its opaque ADH-... id, matching how
// entries resolve ConvName.
func TestLRPAdResolvesRoomTitle(t *testing.T) {
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	s := New(Config{Character: "Vix", Renderer: renderer})
	s.ensureConv(model.ConvRef{Kind: model.ConvRoom, ID: "adh-abc12345"}).title = "Private Room"
	frame := jsonFrame("LRP", `{"character":"Kira","channel":"adh-abc12345","message":"hello"}`)
	if err := s.handle(frame); err != nil {
		t.Fatal(err)
	}
	ads := s.Ads()
	if len(ads) != 1 {
		t.Fatalf("ads = %+v", ads)
	}
	if ads[0].Channel != "Private Room" {
		t.Fatalf("channel = %q, want %q", ads[0].Channel, "Private Room")
	}
}

// TestLRPAdKeepsUnknownRoomID verifies an ad from a room the session has no
// title for falls back to the raw channel instead of an empty label.
func TestLRPAdKeepsUnknownRoomID(t *testing.T) {
	s := New(Config{Character: "Vix"})
	frame := jsonFrame("LRP", `{"character":"Kira","channel":"adh-unknown1","message":"hi"}`)
	if err := s.handle(frame); err != nil {
		t.Fatal(err)
	}
	ads := s.Ads()
	if len(ads) != 1 || ads[0].Channel != "adh-unknown1" {
		t.Fatalf("ads = %+v, want raw channel", ads)
	}
}
