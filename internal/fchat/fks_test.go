package fchat

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestFKSRequestAlwaysMarshalsKinks: the server treats "kinks" as required, so
// a nil slice must still serialize as [], never null.
func TestFKSRequestAlwaysMarshalsKinks(t *testing.T) {
	frame, err := New("FKS", FKSRequest{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(frame.Data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(raw["kinks"]); got != "[]" {
		t.Fatalf("kinks = %s, want []", got)
	}
	got, err := Decode[FKSRequest](frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Kinks == nil || len(got.Kinks) != 0 {
		t.Fatalf("decoded kinks = %#v, want empty non-nil", got.Kinks)
	}
}

func TestFKSRoundTrip(t *testing.T) {
	req := FKSRequest{
		Kinks:        []int{523, 66},
		Genders:      []string{"Male", "Maleherm"},
		Orientations: []string{"Gay", "Bi - male preference"},
		Languages:    []string{"Dutch"},
		FurryPrefs:   []string{"No humans, just furry characters"},
		Roles:        []string{"Always dominant"},
	}
	frame, err := New("FKS", req)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gotReq, err := Decode[FKSRequest](frame)
	if err != nil {
		t.Fatalf("Decode request: %v", err)
	}
	if !reflect.DeepEqual(gotReq, req) {
		t.Fatalf("request round trip = %+v, want %+v", gotReq, req)
	}

	ev := FKSEvent{Characters: []string{"Some Guy", "Some Gal"}, Kinks: []int{523, 66}}
	frame, err = New("FKS", ev)
	if err != nil {
		t.Fatalf("New event: %v", err)
	}
	gotEv, err := Decode[FKSEvent](frame)
	if err != nil {
		t.Fatalf("Decode event: %v", err)
	}
	if !reflect.DeepEqual(gotEv, ev) {
		t.Fatalf("event round trip = %+v, want %+v", gotEv, ev)
	}
}
