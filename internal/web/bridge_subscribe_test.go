package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"plexo/internal/broker"
	"plexo/internal/core"
	"plexo/internal/model"
	"plexo/test/memstore"
)

// newBridgeServer wires a manager to a bridge and an httptest server, returning
// both so tests can tune the grace window and inspect the registry.
func newBridgeServer(t *testing.T) (*Bridge, string, func()) {
	t.Helper()
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	bridge := NewBridge(manager, core.NewAccount(), NewSessionAuth(""), nil)
	mux := http.NewServeMux()
	mux.Handle("/ws", bridge.Handler())
	srv := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	return bridge, wsURL, func() {
		srv.Close()
		bridge.close()
	}
}

// dialSubscribed dials the bridge and sends the subscribe envelope naming the
// durable subscription this socket attaches to.
func dialSubscribed(t *testing.T, ctx context.Context, wsURL, id string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	out, _ := json.Marshal(Envelope{Type: TSubscribe, Data: mustJSON(Subscribe{ID: id})})
	if err := c.Write(ctx, websocket.MessageText, out); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	return c
}

// sendInterest sets a conversation's interest and waits for its ack.
func sendInterest(t *testing.T, ctx context.Context, c *websocket.Conn, cid, session string, conv model.ConvRef, level model.Interest) {
	t.Helper()
	cmd := model.Command{CID: cid, Op: model.OpSetInterest, Session: session, Conv: conv, Level: level}
	out, _ := json.Marshal(Envelope{Type: TCmd, CID: cid, Data: mustJSON(cmd)})
	if err := c.Write(ctx, websocket.MessageText, out); err != nil {
		t.Fatalf("write set_interest: %v", err)
	}
	for {
		env := readEnvelope(t, ctx, c)
		if (env.Type != TAck && env.Type != TErr) || env.CID != cid {
			continue
		}
		return
	}
}

// waitForBatch reads until a batch delivers an event of the given kind.
func waitForBatch(t *testing.T, ctx context.Context, c *websocket.Conn, kind model.EventKind) model.Event {
	t.Helper()
	for {
		env := readEnvelope(t, ctx, c)
		if env.Type != TBatch {
			continue
		}
		var b broker.Batch
		if err := json.Unmarshal(env.Data, &b); err != nil {
			t.Fatalf("decode batch: %v", err)
		}
		for _, ev := range b.Events {
			if ev.Kind == kind {
				return ev
			}
		}
	}
}

// waitRegistered waits until the registry entry for id exists and is attached,
// so a test can trust a subscribe it sent has been processed by the server.
func waitRegistered(t *testing.T, b *Bridge, id string) *broker.Subscription {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.subsMu.Lock()
		e, ok := b.subs[id]
		var sub *broker.Subscription
		if ok && e.attached {
			sub = e.sub
		}
		b.subsMu.Unlock()
		if sub != nil {
			return sub
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscription %q was never registered", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitDetached waits until the registry entry for id exists and is detached,
// so a reconnect does not race the server noticing the old socket closed.
func waitDetached(t *testing.T, b *Bridge, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.subsMu.Lock()
		e, ok := b.subs[id]
		detached := ok && !e.attached
		b.subsMu.Unlock()
		if detached {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscription %q never detached", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestBridgeReconnectPreservesInterest: a socket that drops and reconnects with
// the same subscribe id resumes the durable subscription, so the interest it
// asserted (full) survives without being re-asserted.
func TestBridgeReconnectPreservesInterest(t *testing.T) {
	bridge, wsURL, stop := newBridgeServer(t)
	defer stop()
	bridge.grace = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	c1 := dialSubscribed(t, ctx, wsURL, "durable-1")
	if env := readEnvelope(t, ctx, c1); env.Type != THello {
		t.Fatalf("first envelope = %q, want hello", env.Type)
	}
	sendInterest(t, ctx, c1, "u-1", "Vix", conv, model.InterestFull)
	c1.CloseNow()
	waitDetached(t, bridge, "durable-1")

	c2 := dialSubscribed(t, ctx, wsURL, "durable-1")
	defer c2.CloseNow()
	if env := readEnvelope(t, ctx, c2); env.Type != THello {
		t.Fatalf("first envelope = %q, want hello", env.Type)
	}

	// No set_interest was sent on c2: full interest must have survived.
	bridge.manager.Broker().Publish(model.Event{
		Session: "Vix", Kind: model.EvMessage,
		Payload: model.MessagePayload{Conv: conv},
	})
	waitForBatch(t, ctx, c2, model.EvMessage)
}

// TestBridgeGraceExpiryClosesSubscription: a detached subscription whose grace
// window elapses is closed rather than held forever.
func TestBridgeGraceExpiryClosesSubscription(t *testing.T) {
	bridge, wsURL, stop := newBridgeServer(t)
	defer stop()
	bridge.grace = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := dialSubscribed(t, ctx, wsURL, "expiring")
	if env := readEnvelope(t, ctx, c); env.Type != THello {
		t.Fatalf("first envelope = %q, want hello", env.Type)
	}
	sub := waitRegistered(t, bridge, "expiring")

	c.CloseNow()

	closed := false
	deadline := time.After(3 * time.Second)
	for !closed {
		select {
		case _, ok := <-sub.Events():
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatal("subscription was not closed after the grace window")
		}
	}
	bridge.subsMu.Lock()
	_, still := bridge.subs["expiring"]
	bridge.subsMu.Unlock()
	if still {
		t.Fatal("expired subscription still in the registry")
	}
}

// TestBridgeNewIDStartsFreshSubscription: a different subscribe id is a new
// subscription at the default (summary) interest, so a message is filtered and
// only its summary arrives — the reload/re-assert path.
func TestBridgeNewIDStartsFreshSubscription(t *testing.T) {
	bridge, wsURL, stop := newBridgeServer(t)
	defer stop()
	bridge.grace = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	c1 := dialSubscribed(t, ctx, wsURL, "old-id")
	if env := readEnvelope(t, ctx, c1); env.Type != THello {
		t.Fatalf("first envelope = %q, want hello", env.Type)
	}
	sendInterest(t, ctx, c1, "u-1", "Vix", conv, model.InterestFull)
	c1.CloseNow()

	c2 := dialSubscribed(t, ctx, wsURL, "new-id")
	defer c2.CloseNow()
	if env := readEnvelope(t, ctx, c2); env.Type != THello {
		t.Fatalf("first envelope = %q, want hello", env.Type)
	}

	bridge.manager.Broker().Publish(model.Event{Session: "Vix", Kind: model.EvMessage, Payload: model.MessagePayload{Conv: conv}})
	bridge.manager.Broker().Publish(model.Event{
		Session: "Vix", Kind: model.EvState,
		Payload: model.StatePayload{Key: model.SummaryKey("Vix", conv), Value: model.SummaryPayload{Conv: conv}},
	})

	for {
		env := readEnvelope(t, ctx, c2)
		if env.Type != TBatch {
			continue
		}
		var b broker.Batch
		if err := json.Unmarshal(env.Data, &b); err != nil {
			t.Fatalf("decode batch: %v", err)
		}
		for _, ev := range b.Events {
			if ev.Kind == model.EvMessage {
				t.Fatal("a new subscription received a full message; it should default to summary")
			}
			if m, ok := ev.Payload.(map[string]any); ok {
				if k, _ := m["key"].(string); model.KeyNamespace(k) == model.StateSummary {
					return
				}
			}
		}
	}
}
