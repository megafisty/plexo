package session

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/fchat"
	"plexo/internal/model"
)

// fakeFriendBookmarkService records the coordinator calls a session makes and
// serves a fixed split for classification.
type fakeFriendBookmarkService struct {
	mu    sync.Mutex
	ready []string
	gone  []string
	rtb   [][2]string

	friendSet   map[string]bool
	bookmarkSet map[string]bool
	fetched     bool
	changed     bool
}

func (f *fakeFriendBookmarkService) SessionReady(character string) {
	f.mu.Lock()
	f.ready = append(f.ready, character)
	f.mu.Unlock()
}

func (f *fakeFriendBookmarkService) SessionGone(character string) {
	f.mu.Lock()
	f.gone = append(f.gone, character)
	f.mu.Unlock()
}

func (f *fakeFriendBookmarkService) ApplyFriendBookmarkRTB(kind, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rtb = append(f.rtb, [2]string{kind, name})
	return f.changed
}

func (f *fakeFriendBookmarkService) Contacts() ContactSplit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ContactSplit{Friends: f.friendSet, Bookmarks: f.bookmarkSet, Fetched: f.fetched}
}

func (f *fakeFriendBookmarkService) snapshot() (ready, gone []string, rtb [][2]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ready...), append([]string(nil), f.gone...), append([][2]string(nil), f.rtb...)
}

// TestFriendBookmarkReadyAndRTBHooks: reaching ready registers the session, and
// realtime-bridge deltas reach the coordinator.
func TestFriendBookmarkReadyAndRTBHooks(t *testing.T) {
	svc := &fakeFriendBookmarkService{}
	s := New(Config{Character: "Vix", FriendBookmarks: svc})
	s.st.phase = "identified"

	if err := s.handle(jsonFrame("NLN", `{"identity":"Vix","gender":"Female","status":"online"}`)); err != nil {
		t.Fatalf("NLN: %v", err)
	}
	if err := s.handle(jsonFrame("RTB", `{"type":"trackadd","name":"Carol"}`)); err != nil {
		t.Fatalf("RTB: %v", err)
	}

	ready, _, rtb := svc.snapshot()
	if len(ready) != 1 || ready[0] != "Vix" {
		t.Fatalf("ready = %v, want [Vix]", ready)
	}
	if len(rtb) != 1 || rtb[0] != [2]string{"trackadd", "Carol"} {
		t.Fatalf("rtb = %v, want [[trackadd Carol]]", rtb)
	}
}

// TestFriendBookmarkSessionGoneOnDisconnect: a terminal disconnect reports the
// session as no longer ready.
func TestFriendBookmarkSessionGoneOnDisconnect(t *testing.T) {
	svc := &fakeFriendBookmarkService{}
	s := New(Config{
		Character:       "Vix",
		FriendBookmarks: svc,
		Dial:            func(context.Context) (fchat.Conn, error) { return nil, fchat.ErrInvalidCredentials },
	})
	s.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, gone, _ := svc.snapshot()
		if len(gone) == 1 && gone[0] == "Vix" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("SessionGone not called, got %v", gone)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFriendBookmarkSplitEmitted: the session partitions the FRL union using
// the coordinator's split, defaults an unclassified name to friends, and lets a
// character that is both appear in both lists.
func TestFriendBookmarkSplitEmitted(t *testing.T) {
	b := broker.New()
	svc := &fakeFriendBookmarkService{
		friendSet:   map[string]bool{nameKey("Aiaru"): true, nameKey("Both"): true},
		bookmarkSet: map[string]bool{nameKey("Carol"): true, nameKey("Both"): true},
	}
	s := New(Config{Character: "Vix", Broker: b, FriendBookmarks: svc})
	s.st.friends[nameKey("Aiaru")] = true // friend
	s.st.friends[nameKey("Carol")] = true // bookmark
	s.st.friends[nameKey("Both")] = true  // friend and bookmark
	s.st.friends[nameKey("Unclassified")] = true
	s.setPresenceQuiet("Aiaru", "", "online", "")
	s.setPresenceQuiet("Carol", "", "online", "")
	s.setPresenceQuiet("Both", "", "online", "")
	s.setPresenceQuiet("Unclassified", "", "online", "")

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()
	s.emitAccountSets()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before friends event")
			}
			for _, ev := range batch.Events {
				sp, ok := stateFor(ev, model.AccountKey("friends"))
				if !ok {
					continue
				}
				p, ok := sp.Value.(model.FriendsPayload)
				if !ok {
					continue
				}
				friends := memberNames(p.Friends)
				bookmarks := memberNames(p.Bookmarks)
				if !slices.Contains(friends, "Aiaru") || !slices.Contains(friends, "Both") || !slices.Contains(friends, "Unclassified") || slices.Contains(friends, "Carol") {
					t.Fatalf("friends = %v", friends)
				}
				if !slices.Contains(bookmarks, "Carol") || !slices.Contains(bookmarks, "Both") || slices.Contains(bookmarks, "Aiaru") || slices.Contains(bookmarks, "Unclassified") {
					t.Fatalf("bookmarks = %v", bookmarks)
				}
				return
			}
		case <-deadline:
			t.Fatal("no friends event")
		}
	}
}

func memberNames(in []model.MemberInfo) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		out = append(out, m.Name)
	}
	return out
}

// TestFriendBookmarkFetchedSplitIsAuthoritative: once the REST split is fetched
// it is the membership source, not the FRL union. A stale union entry the split
// no longer lists is dropped, and a name the split knows but the union never had
// is projected.
func TestFriendBookmarkFetchedSplitIsAuthoritative(t *testing.T) {
	svc := &fakeFriendBookmarkService{
		fetched:     true,
		friendSet:   map[string]bool{nameKey("Aiaru"): true, nameKey("Both"): true},
		bookmarkSet: map[string]bool{nameKey("Carol"): true, nameKey("Both"): true},
	}
	s := New(Config{Character: "Vix", FriendBookmarks: svc})
	s.st.friends[nameKey("Aiaru")] = true
	s.st.friends[nameKey("Both")] = true
	s.st.friends[nameKey("Removed")] = true
	for _, name := range []string{"Aiaru", "Both", "Carol", "Removed"} {
		s.setPresenceQuiet(name, "", "online", "")
	}

	friends, bookmarks := s.friendBookmarkInfosLocked()
	fr := memberNames(friends)
	bm := memberNames(bookmarks)
	if !slices.Contains(fr, "Aiaru") || !slices.Contains(fr, "Both") || slices.Contains(fr, "Carol") || slices.Contains(fr, "Removed") {
		t.Fatalf("friends = %v", fr)
	}
	if !slices.Contains(bm, "Carol") || !slices.Contains(bm, "Both") || slices.Contains(bm, "Aiaru") || slices.Contains(bm, "Removed") {
		t.Fatalf("bookmarks = %v", bm)
	}
}
