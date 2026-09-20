package session

import (
	"context"
	"testing"
	"time"

	"plexo/internal/broker"
	"plexo/internal/fchat"
	"plexo/internal/model"
)

func jsonFrame(code, body string) fchat.Frame {
	return fchat.Frame{Code: code, Data: []byte(body)}
}

// waitFriends fails unless the subscription delivers a friends event listing
// the named character within the deadline.
func waitFriends(t *testing.T, sub *broker.Subscription, want string) {
	t.Helper()
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
				for _, f := range p.Friends {
					if f.Name == want {
						return
					}
				}
			}
		case <-deadline:
			t.Fatalf("did not observe a friends event containing %q", want)
		}
	}
}

// TestRTBUpdatesFriends: the realtime bridge's bookmark/friend deltas update
// the account FRL union and republish it.
func TestRTBUpdatesFriends(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})
	s.st.friends[nameKey("Aiaru")] = true
	// Friends only reach the client once the roster can name them
	// authoritatively; RTB carries no presence, so seed both as online.
	s.setPresenceQuiet("Aiaru", "", "online", "")
	s.setPresenceQuiet("Bob", "", "online", "")

	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	if err := s.handle(jsonFrame("RTB", `{"type":"trackadd","name":"Bob"}`)); err != nil {
		t.Fatalf("RTB trackadd: %v", err)
	}
	if !s.st.friends[nameKey("Bob")] {
		t.Fatal("trackadd did not add Bob to the FRL union")
	}
	waitFriends(t, sub, "Bob")

	if err := s.handle(jsonFrame("RTB", `{"type":"friendremove","name":"Aiaru"}`)); err != nil {
		t.Fatalf("RTB friendremove: %v", err)
	}
	if s.st.friends[nameKey("Aiaru")] {
		t.Fatal("friendremove did not drop Aiaru from the FRL union")
	}
}

// TestLISRefreshesFriendPresence: LIS hydrates the roster quietly. A presence
// refresh for a friend must stream as a presence event even though the friends
// set itself is unchanged (the friends event is de-duplicated by name set).
// FRL arriving before the LIS batch must still yield an online friend.
func TestLISRefreshesFriendPresence(t *testing.T) {
	b := broker.New()
	s := New(Config{Character: "Vix", Broker: b})

	// FRL first, while the roster is still empty: the friend is offline and
	// named with a lowercased spelling, like an ignore-list entry.
	if err := s.handle(jsonFrame("FRL", `{"characters":["bestfriend"]}`)); err != nil {
		t.Fatalf("FRL: %v", err)
	}

	// The subscriber connects after FRL, so it never saw that event.
	sub := b.Subscribe(broker.SubOpts{DefaultInterest: model.InterestFull})
	defer sub.Close()

	// LIS is authoritative for the spelling; the lowercased FRL seed must not
	// win the roster key.
	if err := s.handle(jsonFrame("LIS", `{"characters":[["BestFriend","Female","online",""]]}`)); err != nil {
		t.Fatalf("LIS: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed before the presence refresh")
			}
			for _, ev := range batch.Events {
				sp, ok := stateFor(ev, model.CharacterKey("BestFriend"))
				if !ok {
					continue
				}
				if p, ok := sp.Value.(model.PresencePayload); ok && p.Character == "BestFriend" && p.Online {
					return
				}
			}
		case <-deadline:
			t.Fatal("LIS did not refresh the friend to online")
		}
	}
}

// TestRTBIgnoresUnknownType: unknown RTB types are discarded, not applied.
func TestRTBIgnoresUnknownType(t *testing.T) {
	s := New(Config{Character: "Vix"})
	if err := s.handle(jsonFrame("RTB", `{"type":"friendrequest","name":"Bob"}`)); err != nil {
		t.Fatalf("RTB friendrequest: %v", err)
	}
	if s.st.friends[nameKey("Bob")] {
		t.Fatal("a friend request must not enter the friends/bookmarks union")
	}
}

// TestIGNInitAndDeltas: the login init replaces the set; add/delete mutate it.
func TestIGNInitAndDeltas(t *testing.T) {
	s := New(Config{Character: "Vix"})

	if err := s.handle(jsonFrame("IGN", `{"characters":["a","b"],"action":"init"}`)); err != nil {
		t.Fatalf("IGN init: %v", err)
	}
	if !s.st.ignores["a"] || !s.st.ignores["b"] || len(s.st.ignores) != 2 {
		t.Fatalf("init did not set the ignore list: %+v", s.st.ignores)
	}

	if err := s.handle(jsonFrame("IGN", `{"character":"c","action":"add"}`)); err != nil {
		t.Fatalf("IGN add: %v", err)
	}
	if !s.st.ignores["c"] {
		t.Fatal("add did not add c")
	}

	if err := s.handle(jsonFrame("IGN", `{"character":"a","action":"delete"}`)); err != nil {
		t.Fatalf("IGN delete: %v", err)
	}
	if s.st.ignores["a"] {
		t.Fatal("delete did not remove a")
	}

	// A second init replaces wholesale, dropping names not in the frame.
	if err := s.handle(jsonFrame("IGN", `{"characters":["z"],"action":"list"}`)); err != nil {
		t.Fatalf("IGN list: %v", err)
	}
	if !s.st.ignores["z"] || len(s.st.ignores) != 1 {
		t.Fatalf("list/init must be set-to: %+v", s.st.ignores)
	}
}

// TestSetIgnoreCommandQueuesIGN: the set_ignore op routes to an IGN frame and
// rejects unknown actions and missing characters.
func TestSetIgnoreCommandQueuesIGN(t *testing.T) {
	s := New(Config{Character: "Vix"})
	s.out = make(chan outbound, 4)
	// A live session always has a context; without one, done() returns an
	// already-closed channel and queue() may race the send against it.
	s.mu.Lock()
	s.ctx = context.Background()
	s.mu.Unlock()

	res := s.handleCommand(model.Command{Op: model.OpSetIgnore, Character: "Bob", Action: "add"})
	if !res.Accepted {
		t.Fatalf("set_ignore add rejected: %+v", res)
	}
	select {
	case ob := <-s.out:
		f := ob.wire
		if f.Code != "IGN" {
			t.Fatalf("expected IGN, got %s", f.Code)
		}
		p, err := fchat.Decode[fchat.IgnoreUpdate](f)
		if err != nil {
			t.Fatalf("decode IGN: %v", err)
		}
		if p.Character != "Bob" || p.Action != "add" {
			t.Fatalf("unexpected IGN payload: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no IGN frame queued")
	}

	if res := s.handleCommand(model.Command{Op: model.OpSetIgnore, Action: "delete"}); res.Accepted {
		t.Fatalf("delete without a character must be rejected: %+v", res)
	}
	if res := s.handleCommand(model.Command{Op: model.OpSetIgnore, Action: "bogus"}); res.Accepted {
		t.Fatalf("unknown action must be rejected: %+v", res)
	}

	// list needs no character.
	if res := s.handleCommand(model.Command{Op: model.OpSetIgnore, Action: "list"}); !res.Accepted {
		t.Fatalf("list rejected: %+v", res)
	}
	select {
	case ob := <-s.out:
		f := ob.wire
		if f.Code != "IGN" {
			t.Fatalf("expected IGN, got %s", f.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("no IGN list frame queued")
	}
}
