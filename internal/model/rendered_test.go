package model

import (
	"encoding/json"
	"testing"
)

// TestRenderedEntryWireOmitsBody guards the delivery shape: the client renders
// HTML and must never receive the raw BBCode body. It also pins the delivered
// fields so entryWire cannot silently drift.
func TestRenderedEntryWireOmitsBody(t *testing.T) {
	e := RenderedEntry{
		Entry: Entry{
			ID:         "e1",
			UpstreamID: "u1",
			Session:    "Vix",
			Conv:       ConvRef{Kind: ConvOfficial, ID: "Frontpage"},
			ConvSeq:    7,
			Kind:       "msg",
			Speaker:    "Kira",
			Body:       "[b]hi[/b]",
		},
		HTML: "<b>hi</b>",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["body"]; ok {
		t.Fatalf("body leaked onto the wire: %s", b)
	}
	if _, ok := m["raw"]; ok {
		t.Fatalf("raw leaked onto the wire: %s", b)
	}
	for _, key := range []string{"id", "upstreamId", "session", "conv", "convSeq", "kind", "speaker", "createdAtMs", "receivedAtMs", "html"} {
		if _, ok := m[key]; !ok {
			t.Errorf("field %q missing from the wire shape: %s", key, b)
		}
	}
	var htmlOut string
	if err := json.Unmarshal(m["html"], &htmlOut); err != nil {
		t.Fatalf("html field: %v", err)
	}
	if htmlOut != "<b>hi</b>" {
		t.Errorf("html = %q, want %q", htmlOut, "<b>hi</b>")
	}
}
