package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/session"
	"plexo/test/fakeserver"
	"plexo/test/memstore"
)

// TestAPIRoom covers the on-demand management read: after a room is created the
// view reports the caller as owner, and the endpoint's guard rails are distinct.
func TestAPIRoom(t *testing.T) {
	fac := fakeserver.NewWSFactory(fakeserver.Options{Character: "Vix"})
	t.Cleanup(fac.Close)
	manager := core.NewManager(context.Background(), core.Config{
		Store:   memstore.New(),
		Tickets: fchat.TicketFunc(testTickets),
		Dial:    func(string) session.Dialer { return session.Dialer(fac.Dial) },
	})
	base := newAPIServer(t, manager, "")
	if err := manager.Login("acct", "Vix"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Logout("Vix") })
	waitLiveTest(t, manager)

	res := manager.Dispatch(model.Command{
		CID: "create", Session: "Vix", Op: model.OpRoomAdmin,
		Room: &model.RoomAdminRequest{Action: "create", Title: "My Room"},
	})
	if !res.Accepted {
		t.Fatalf("create rejected: %+v", res)
	}

	// The room id arrives with the server's self JCH; poll the snapshot until
	// the room conversation exists.
	var roomID string
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, s := range manager.Snapshot().Sessions {
			for _, c := range s.Conversations {
				if c.Kind == model.ConvRoom {
					roomID = c.Conv.ID
				}
			}
		}
		if roomID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("room never appeared: %+v", manager.Snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}

	resp, err := http.Get(base + "/api/room?session=Vix&conv_kind=room&conv_id=" + roomID)
	if err != nil {
		t.Fatalf("get room: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var info model.RoomInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode room: %v", err)
	}
	if info.SelfRole != model.RoomRoleOwner {
		t.Fatalf("selfRole = %q, want owner", info.SelfRole)
	}
	if info.Title != "My Room" {
		t.Fatalf("title = %q, want My Room", info.Title)
	}
	if info.Ops == nil || info.Bans == nil {
		t.Fatalf("ops/bans must be set-to lists: %+v", info)
	}

	// Unknown session and an unjoined room are distinct failures.
	if r, _ := http.Get(base + "/api/room?session=Nobody&conv_kind=room&conv_id=x"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", r.StatusCode)
	}
	if r, _ := http.Get(base + "/api/room?session=Vix&conv_kind=room&conv_id=ADH-missing"); r.StatusCode != http.StatusConflict {
		t.Fatalf("unjoined room status = %d, want 409", r.StatusCode)
	}
	if r, _ := http.Get(base + "/api/room?session=Vix&conv_kind=dm&conv_id=Kira"); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("dm kind status = %d, want 400", r.StatusCode)
	}
}

func waitLiveTest(t *testing.T, manager *core.Manager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := manager.Snapshot()
		if len(snap.Sessions) == 1 && snap.Sessions[0].State == "live" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never became live: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
