package session

import (
	"context"
	"testing"
	"time"

	"plexo/internal/model"
	"plexo/internal/store"
	"plexo/test/memstore"
)

// TestPersistBatchesAndSyncBarrier: entries are written asynchronously, Sync
// makes every entry enqueued before it durable before returning, and the stop
// sentinel flushes the tail. It drives runPersist directly so the batching
// contract is checked without a live connection.
func TestPersistBatchesAndSyncBarrier(t *testing.T) {
	st := memstore.New()
	s := New(Config{Character: "Vix", Store: st})
	// Mimic the started state so Sync uses the queue and a live ctx.
	s.mu.Lock()
	s.ctx = context.Background()
	s.persist = make(chan persistItem, persistQueue)
	ch := s.persist
	s.mu.Unlock()
	go s.runPersist(ch)

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	for i := 1; i <= 3; i++ {
		ch <- persistItem{entry: model.Entry{
			ID: "e" + string(rune('0'+i)), Session: "Vix", Conv: conv,
			ConvSeq: uint64(i), Kind: "msg", Body: "m",
		}}
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, err := st.History(context.Background(), store.HistoryQuery{Session: "Vix", Conv: conv})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 3 || got[2].ConvSeq != 3 {
		t.Fatalf("history after Sync = %+v", got)
	}

	// The stop sentinel flushes the pending tail before the writer exits.
	ch <- persistItem{entry: model.Entry{ID: "tail", Session: "Vix", Conv: conv, ConvSeq: 4, Kind: "msg", Body: "t"}}
	ch <- persistItem{stop: true}
	deadline := time.Now().Add(time.Second)
	for {
		got, _ = st.History(context.Background(), store.HistoryQuery{Session: "Vix", Conv: conv})
		if len(got) == 4 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail not flushed on stop; history = %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
}
