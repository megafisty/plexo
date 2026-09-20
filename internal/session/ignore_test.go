package session

import (
	"testing"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
)

// ignoredTestSession builds a live session with a buffered outbound channel so
// handle() can answer with frames without a running writer.
func ignoredTestSession(t *testing.T) *Session {
	t.Helper()
	s := New(Config{Character: "Vix"})
	s.out = make(chan outbound, 8)
	liveCtx(t, s)
	return s
}

// takeOutbound fails unless a frame is queued within the deadline.
func takeOutbound(t *testing.T, s *Session) fchat.Frame {
	t.Helper()
	select {
	case ob := <-s.out:
		return ob.wire
	case <-time.After(time.Second):
		t.Fatal("no outbound frame queued")
		return fchat.Frame{}
	}
}

// TestPRIFromIgnoredSenderIsNotifiedAndDropped: the official server does not
// filter DMs by the ignore list, so the core must answer with IGN notify (which
// the server relays to the sender as ERR 20) and drop the message before it is
// recorded or shown.
func TestPRIFromIgnoredSenderIsNotifiedAndDropped(t *testing.T) {
	s := ignoredTestSession(t)
	s.st.ignores[nameKey("Kira")] = true

	if err := s.handle(jsonFrame("PRI", `{"character":"Kira","recipient":"Vix","message":"hi"}`)); err != nil {
		t.Fatalf("PRI: %v", err)
	}

	f := takeOutbound(t, s)
	if f.Code != "IGN" {
		t.Fatalf("outbound frame = %s, want IGN", f.Code)
	}
	p, err := fchat.Decode[fchat.IgnoreNotify](f)
	if err != nil {
		t.Fatalf("decode IGN: %v", err)
	}
	if p.Character != "Kira" || p.Action != "notify" {
		t.Fatalf("IGN payload = %+v, want {Kira notify}", p)
	}

	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	if _, ok := s.st.convs[convKey(dm)]; ok {
		t.Fatal("ignored DM created a conversation")
	}
	if len(s.st.typing) != 0 {
		t.Fatalf("ignored DM touched typing state: %+v", s.st.typing)
	}
	select {
	case ob := <-s.out:
		t.Fatalf("unexpected extra outbound frame: %s", ob.wire.Code)
	default:
	}
}

// TestPRIFromIgnoredSenderIsCaseInsensitive: both the ignore set and the wire
// name are case-folded.
func TestPRIFromIgnoredSenderIsCaseInsensitive(t *testing.T) {
	s := ignoredTestSession(t)
	s.st.ignores[nameKey("Kira")] = true

	if err := s.handle(jsonFrame("PRI", `{"character":"kira","recipient":"Vix","message":"hi"}`)); err != nil {
		t.Fatalf("PRI: %v", err)
	}
	p, err := fchat.Decode[fchat.IgnoreNotify](takeOutbound(t, s))
	if err != nil || p.Action != "notify" {
		t.Fatalf("notify = %+v, err %v", p, err)
	}
}

// TestPRIFromUnignoredSenderIsRecorded: a normal DM still records and queues no
// ignore notify.
func TestPRIFromUnignoredSenderIsRecorded(t *testing.T) {
	s := ignoredTestSession(t)

	if err := s.handle(jsonFrame("PRI", `{"character":"Bob","recipient":"Vix","message":"hey"}`)); err != nil {
		t.Fatalf("PRI: %v", err)
	}
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Bob"}
	if _, ok := s.st.convs[convKey(dm)]; !ok {
		t.Fatal("unignored DM did not create a conversation")
	}
	select {
	case ob := <-s.out:
		t.Fatalf("unexpected outbound frame: %s", ob.wire.Code)
	default:
	}
}

// TestPRIFromRemovedIgnoredSenderIsRecorded: deleting an ignore re-enables DMs.
func TestPRIFromRemovedIgnoredSenderIsRecorded(t *testing.T) {
	s := ignoredTestSession(t)
	s.st.ignores[nameKey("Kira")] = true

	if err := s.handle(jsonFrame("IGN", `{"action":"delete","character":"Kira"}`)); err != nil {
		t.Fatalf("IGN delete: %v", err)
	}
	if err := s.handle(jsonFrame("PRI", `{"character":"Kira","recipient":"Vix","message":"hi"}`)); err != nil {
		t.Fatalf("PRI: %v", err)
	}
	dm := model.ConvRef{Kind: model.ConvDM, ID: "Kira"}
	if _, ok := s.st.convs[convKey(dm)]; !ok {
		t.Fatal("DM from a removed ignore was dropped")
	}
}
