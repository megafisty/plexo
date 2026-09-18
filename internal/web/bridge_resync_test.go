package web

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"plexo/internal/core"
	"plexo/internal/model"
	"plexo/test/memstore"
)

// TestBridgeDroppedBatchResyncedBySubscription: when an outbound batch cannot
// be queued, the bridge hands the events back to the subscription, which
// re-emits the latest value per key on its next tick. No snapshot is involved.
func TestBridgeDroppedBatchResyncedBySubscription(t *testing.T) {
	manager := core.NewManager(context.Background(), core.Config{Store: memstore.New()})
	sub := manager.Subscribe()
	defer sub.Close()

	c := &client{
		bridge: NewBridge(manager, core.NewAccount(), NewSessionAuth(""), nil),
		send:   make(chan Envelope, 1),
		urgent: make(chan Envelope, 1),
		done:   make(chan struct{}),
		sub:    sub,
	}
	defer close(c.done)

	// Fill the only send slot so the next batch is dropped.
	c.send <- Envelope{Type: THello}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.forwardBatches(ctx)

	key := model.SessionKey("Ghost")
	manager.Broker().Publish(model.Event{
		Session: "Ghost", Kind: model.EvState,
		Payload: model.StatePayload{Key: key, Value: model.SessionStatePayload{State: "live"}},
	})

	// Let forwardBatches attempt the (full) send lane and hand the event back.
	time.Sleep(250 * time.Millisecond)
	<-c.send // free the slot

	deadline := time.After(2 * time.Second)
	for {
		select {
		case env := <-c.send:
			if env.Type != TBatch {
				continue
			}
			var batch struct {
				Events []struct {
					Kind    string `json:"kind"`
					Payload struct {
						Key string `json:"key"`
					} `json:"payload"`
				} `json:"events"`
			}
			if err := json.Unmarshal(env.Data, &batch); err != nil {
				t.Fatalf("decode batch: %v", err)
			}
			for _, ev := range batch.Events {
				if ev.Kind == string(model.EvState) && ev.Payload.Key == key {
					return
				}
			}
		case <-deadline:
			t.Fatal("no keyed resync after a dropped batch")
		}
	}
}
