package broker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"plexo/internal/model"
)

// stateEvent builds one set-to record for a key.
func stateEvent(session, key string, value any) model.Event {
	return model.Event{Session: session, Kind: model.EvState, Payload: model.StatePayload{Key: key, Value: value}}
}

// presenceEvent builds one character presence record.
func presenceEvent(char string) model.Event {
	return stateEvent("Vix", model.CharacterKey(char), model.PresencePayload{Character: char, Online: true})
}

// TestBrokerCloseWithSubscribers guards the self-deadlock where Close held the
// broker lock while Subscription.Close tried to take it.
func TestBrokerCloseWithSubscribers(t *testing.T) {
	b := New()
	s := b.Subscribe(DefaultSubOpts())
	b.Publish(stateEvent("Vix", model.SessionKey("Vix"), model.SessionStatePayload{State: "live"}))

	done := make(chan struct{})
	go func() {
		b.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broker.Close deadlocked with live subscribers")
	}

	// Close is idempotent.
	b.Close()

	// The subscription's event channel must close promptly (after any final
	// buffered batch).
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-s.Events():
			if !ok {
				return // closed as expected
			}
		case <-deadline:
			t.Fatal("events channel was not closed")
		}
	}
}

// TestSetInterestSurvivesBusyQueue: an interest change must be delivered even
// when the subscription's input queue is full — the run loop drains promptly,
// so a blocking send resolves.
func TestSetInterestSurvivesBusyQueue(t *testing.T) {
	b := New()
	s := b.Subscribe(DefaultSubOpts())
	defer s.Close()

	// Flood the input queue; SetInterest must still land.
	msg := presenceEvent("X")
	for i := 0; i < 4096; i++ {
		b.Publish(msg)
	}
	s.SetInterest("Vix", model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}, model.InterestFull, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.policy.interestFor("Vix", model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}) == model.InterestFull {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("interest change was dropped")
}

// TestSubscriptionDirtyResets: the pending resync set is consume-once and
// returns exactly what was dropped.
func TestSubscriptionDirtyResets(t *testing.T) {
	b := New()
	s := b.Subscribe(DefaultSubOpts())
	defer s.Close()

	if keys, all := s.TakeDirty(); len(keys) != 0 || all {
		t.Fatal("fresh subscription reported dirty")
	}
	s.markDirty(presenceEvent("X"))
	keys, all := s.TakeDirty()
	if all || len(keys) != 1 || keys[0] != model.CharacterKey("X") {
		t.Fatalf("TakeDirty = %v, all=%v; want character/X", keys, all)
	}
	if keys, all := s.TakeDirty(); len(keys) != 0 || all {
		t.Fatal("dirty set was not reset")
	}
}

type stubBuilder struct{ view model.ConvView }

func (s stubBuilder) ConvView(context.Context, string, model.ConvRef, int, uint64) (model.ConvView, error) {
	return s.view, nil
}

func waitForEvent(t *testing.T, sub *Subscription, kind model.EventKind) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind == kind {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

// waitForState reads until the given state key is delivered.
func waitForState(t *testing.T, sub *Subscription, key string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvState {
					continue
				}
				if sp, ok := ev.Payload.(model.StatePayload); ok && sp.Key == key {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %q", key)
		}
	}
}

// TestPresenceScopedToFullInterest: only members of a full-interest
// conversation (and the client's own character) are delivered.
func TestPresenceScopedToFullInterest(t *testing.T) {
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	b := New()
	b.SetViewBuilder(stubBuilder{view: model.ConvView{
		Session: "Vix", Conv: conv, Members: []model.MemberInfo{{Name: "Alice"}},
	}})
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	sub.SetInterest("Vix", conv, model.InterestFull, 0)
	waitForEvent(t, sub, model.EvConvView)

	b.Publish(presenceEvent("Alice"))
	b.Publish(presenceEvent("Bob")) // not a member
	b.Publish(presenceEvent("Vix")) // self, always delivered

	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for !(seen["Alice"] && seen["Vix"]) {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvState {
					continue
				}
				sp := ev.Payload.(model.StatePayload)
				p, ok := sp.Value.(model.PresencePayload)
				if !ok {
					continue
				}
				if p.Character == "Bob" {
					t.Fatal("unwatched presence was delivered")
				}
				seen[p.Character] = true
			}
		case <-deadline:
			t.Fatalf("timed out; seen=%v", seen)
		}
	}
}

// TestPresenceScopedOnMembershipChange: a conv state record updates the watch
// set.
func TestPresenceScopedOnMembershipChange(t *testing.T) {
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	b := New()
	b.SetViewBuilder(stubBuilder{view: model.ConvView{Session: "Vix", Conv: conv}})
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	sub.SetInterest("Vix", conv, model.InterestFull, 0)
	waitForEvent(t, sub, model.EvConvView)

	b.Publish(stateEvent("Vix", model.ConvKey("Vix", conv), model.ConvStatePayload{
		Conv: conv, Members: []string{"Alice"},
	}))
	waitForState(t, sub, model.ConvKey("Vix", conv))

	b.Publish(presenceEvent("Alice"))
	waitForState(t, sub, model.CharacterKey("Alice"))
}

// TestFriendPresenceReachesLateSubscriber: friends/bookmarks are account-wide,
// so a subscription created after the login FRL burst must still stream their
// presence. The broker tracks the set globally and seeds new subscriptions.
func TestFriendPresenceReachesLateSubscriber(t *testing.T) {
	b := New()
	// The session reports the full watch set separately from the filtered
	// client payload; the broker must watch offline friends too.
	b.SetAccountFriends([]string{"BestFriend"})

	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	b.Publish(presenceEvent("BestFriend"))
	waitForState(t, sub, model.CharacterKey("BestFriend"))
}

// TestWatchedAcrossConversations: a character watched via two full-interest
// conversations stays watched until both drop it. Membership is broker-owned;
// the policy only resolves interest.
func TestWatchedAcrossConversations(t *testing.T) {
	b := New()
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	convA := model.ConvRef{Kind: model.ConvOfficial, ID: "A"}
	convB := model.ConvRef{Kind: model.ConvOfficial, ID: "B"}
	sub.policy.setInterest("Vix", convA, model.InterestFull)
	sub.policy.setInterest("Vix", convB, model.InterestFull)

	b.seedConvMembers("Vix", convA, []string{"Alice", "Bob"})
	b.seedConvMembers("Vix", convB, []string{"Alice"})
	if !sub.policy.watched("Vix", "Alice") || !sub.policy.watched("Vix", "Bob") {
		t.Fatal("members should be watched")
	}

	// Leaving conv A drops only its members; Alice stays watched via B.
	b.applyConvState("Vix", model.StatePayload{Key: model.ConvKey("Vix", convA), Removed: true})
	if !sub.policy.watched("Vix", "Alice") {
		t.Fatal("Alice should still be watched via conv B")
	}
	if sub.policy.watched("Vix", "Bob") {
		t.Fatal("Bob should be unwatched")
	}

	b.applyConvState("Vix", model.StatePayload{Key: model.ConvKey("Vix", convB), Removed: true})
	if sub.policy.watched("Vix", "Alice") {
		t.Fatal("Alice should be unwatched")
	}
}

// panicBuilder panics on the first view build, then succeeds.
type panicBuilder struct{ calls int32 }

func (p *panicBuilder) ConvView(_ context.Context, session string, conv model.ConvRef, _ int, _ uint64) (model.ConvView, error) {
	if atomic.AddInt32(&p.calls, 1) == 1 {
		panic("view build exploded")
	}
	return model.ConvView{Session: session, Conv: conv}, nil
}

// TestSubscriptionSurvivesViewPanic: a panic while building a view must not
// silently kill delivery. The failure is surfaced as an error and the loop
// keeps running.
func TestSubscriptionSurvivesViewPanic(t *testing.T) {
	b := New()
	b.SetViewBuilder(&panicBuilder{})
	s := b.Subscribe(DefaultSubOpts())
	defer s.Close()

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	s.SetInterest("Vix", conv, model.InterestFull, 0)

	// The failed build surfaces an error rather than killing delivery.
	waitForEvent(t, s, model.EvError)

	// Delivery still works after the panic.
	b.Publish(stateEvent("Vix", model.SessionKey("Vix"), model.SessionStatePayload{State: "live"}))
	waitForState(t, s, model.SessionKey("Vix"))
}

// TestAccountSetDeDuplicates: friends/ignores are account-wide. A second
// session reporting the same set must not fan out a duplicate, but a real
// change must be forwarded.
func TestAccountSetDeDuplicates(t *testing.T) {
	b := New()
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	report := func(names ...string) {
		out := make([]model.MemberInfo, len(names))
		for i, n := range names {
			out[i] = model.MemberInfo{Name: n}
		}
		b.Publish(stateEvent("Vix", model.AccountKey("friends"), model.FriendsPayload{Friends: out}))
	}

	report("Alice", "Bob")
	if !waitFriendSet(t, sub) {
		t.Fatal("first account set was not forwarded")
	}
	report("alice", "bob") // same names, different casing
	if waitFriendSet(t, sub) {
		t.Fatal("duplicate account set was forwarded")
	}
	report("Alice", "Bob", "Carol")
	if !waitFriendSet(t, sub) {
		t.Fatal("changed account set was not forwarded")
	}
}

// TestFullInterestSuppressesSummary: a full-interest conversation receives the
// stream entry, not a second summary state record for the same message.
func TestFullInterestSuppressesSummary(t *testing.T) {
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	b := New()
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()
	sub.SetInterest("Vix", conv, model.InterestFull, 0)

	b.Publish(model.Event{Session: "Vix", Kind: model.EvMessage, Payload: model.MessagePayload{Conv: conv}})
	b.Publish(stateEvent("Vix", model.SummaryKey("Vix", conv), model.SummaryPayload{Conv: conv}))

	deadline := time.After(300 * time.Millisecond)
	sawMessage := false
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind == model.EvState {
					if sp, ok := ev.Payload.(model.StatePayload); ok && sp.Key == model.SummaryKey("Vix", conv) {
						t.Fatal("summary was delivered to a full-interest subscriber")
					}
				}
				if ev.Kind == model.EvMessage {
					sawMessage = true
				}
			}
		case <-deadline:
			if !sawMessage {
				t.Fatal("message was not delivered at full interest")
			}
			return
		}
	}
}

// gatedBuilder blocks a view build until released, and reports when a build
// starts, so a test can inject an event while the build is in flight.
type gatedBuilder struct {
	entered chan struct{}
	release chan struct{}
	view    model.ConvView
}

func (g *gatedBuilder) ConvView(ctx context.Context, _ string, _ model.ConvRef, _ int, _ uint64) (model.ConvView, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return g.view, nil
	case <-ctx.Done():
		return model.ConvView{}, ctx.Err()
	}
}

// TestViewPrecedesEventsHeldDuringBuild: while a conversation materializes off
// the delivery goroutine, live events for the session are held and emitted
// after the view, so the client never sees torn state.
func TestViewPrecedesEventsHeldDuringBuild(t *testing.T) {
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	b := New()
	builder := &gatedBuilder{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		view:    model.ConvView{Session: "Vix", Conv: conv, Members: []model.MemberInfo{{Name: "Alice"}}},
	}
	b.SetViewBuilder(builder)
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	sub.SetInterest("Vix", conv, model.InterestFull, 0)
	select {
	case <-builder.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("view build did not start")
	}

	// The build is in flight; this message must be held until after the view.
	b.Publish(model.Event{Session: "Vix", Kind: model.EvMessage, Payload: model.MessagePayload{Conv: conv}})
	close(builder.release)

	var order []model.EventKind
	deadline := time.After(2 * time.Second)
	for len(order) < 2 {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind == model.EvConvView || ev.Kind == model.EvMessage {
					order = append(order, ev.Kind)
				}
			}
		case <-deadline:
			t.Fatalf("timed out; order=%v", order)
		}
	}
	if order[0] != model.EvConvView || order[1] != model.EvMessage {
		t.Fatalf("delivery order = %v, want view before the held message", order)
	}
}

// TestFriendPresenceDoesNotRefanList: a friends report whose name set is
// unchanged but whose inline presence changed must not fan out the whole list;
// presence streams as presence records instead.
func TestFriendPresenceDoesNotRefanList(t *testing.T) {
	b := New()
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	report := func(online bool) {
		b.Publish(stateEvent("Vix", model.AccountKey("friends"), model.FriendsPayload{
			Friends: []model.MemberInfo{{Name: "Alice", Online: online}},
		}))
	}

	report(false)
	if !waitFriendSet(t, sub) {
		t.Fatal("first friends set was not forwarded")
	}
	report(true)
	if waitFriendSet(t, sub) {
		t.Fatal("a presence-only friends report re-fanned the list")
	}
}

// TestDirtyStateResyncedFromStore: a state record dropped by the consumer is
// re-emitted from the broker's shared store on the next tick.
func TestDirtyStateResyncedFromStore(t *testing.T) {
	b := New()
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	ev := stateEvent("Vix", model.AccountKey("friends"), model.FriendsPayload{
		Friends: []model.MemberInfo{{Name: "Alice"}},
	})
	b.Publish(ev)
	waitForState(t, sub, model.AccountKey("friends"))

	// Simulate a dropped delivery: the consumer hands the event back.
	sub.MarkDirtyBatch([]model.Event{ev})
	waitForState(t, sub, model.AccountKey("friends"))
}

// TestDirtyStreamRematerialized: a dropped stream entry cannot be replayed, so
// the subscription re-materializes the conversation instead.
func TestDirtyStreamRematerialized(t *testing.T) {
	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	b := New()
	b.SetViewBuilder(stubBuilder{view: model.ConvView{Session: "Vix", Conv: conv}})
	sub := b.Subscribe(DefaultSubOpts())
	defer sub.Close()

	sub.SetInterest("Vix", conv, model.InterestFull, 0)
	waitForEvent(t, sub, model.EvConvView)

	sub.MarkDirtyBatch([]model.Event{model.Event{
		Session: "Vix", Kind: model.EvMessage, Payload: model.MessagePayload{Conv: conv},
	}})
	// A fresh materialization arrives.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind == model.EvConvView {
					return
				}
			}
		case <-deadline:
			t.Fatal("dropped stream entry did not trigger a re-materialization")
		}
	}
}

func waitFriendSet(t *testing.T, sub *Subscription) bool {
	t.Helper()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case batch, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscription closed")
			}
			for _, ev := range batch.Events {
				if ev.Kind != model.EvState {
					continue
				}
				if sp, ok := ev.Payload.(model.StatePayload); ok && sp.Key == model.AccountKey("friends") {
					return true
				}
			}
		case <-deadline:
			return false
		}
	}
}

// TestDropSessionPrunesUnreachablePresence: presence records for characters
// that were only members of a dropped session's conversations are unreachable
// once that session is gone; DropSession must delete them so the state store
// (and a later broad resync) does not grow without bound.
func TestDropSessionPrunesUnreachablePresence(t *testing.T) {
	b := New()
	sub := b.Subscribe(SubOpts{FlushEvery: time.Hour})
	defer sub.Close()

	conv := model.ConvRef{Kind: model.ConvOfficial, ID: "Frontpage"}
	// Register membership through a conversation state record.
	b.Publish(stateEvent("Vix", model.ConvKey("Vix", conv), model.ConvStatePayload{
		Conv:    conv,
		Members: []string{"Kira"},
		Ops:     []string{},
	}))
	b.Publish(presenceEvent("Kira"))
	// A friend is watched account-wide, so its presence must survive.
	b.Publish(stateEvent("Vix", model.AccountKey("friends"), model.FriendsPayload{
		Friends: []model.MemberInfo{{Name: "BestFriend"}},
	}))
	b.Publish(presenceEvent("BestFriend"))

	if _, ok := b.state(model.CharacterKey("Kira")); !ok {
		t.Fatal("member presence was not stored")
	}
	if _, ok := b.state(model.CharacterKey("BestFriend")); !ok {
		t.Fatal("friend presence was not stored")
	}

	b.DropSession("Vix")

	if _, ok := b.state(model.CharacterKey("Kira")); ok {
		t.Fatal("unreachable member presence survived DropSession")
	}
	if _, ok := b.state(model.CharacterKey("BestFriend")); !ok {
		t.Fatal("friend presence was pruned by DropSession")
	}
}

// TestWindowLimitMatchesClientWindow pins the materialization cap to the
// client's retained timeline window. A larger core limit would ship entries the
// client trims away -- pure waste on every conversation switch.
func TestWindowLimitMatchesClientWindow(t *testing.T) {
	if got := DefaultSubOpts().WindowLimit; got != DefaultWindowLimit {
		t.Fatalf("WindowLimit = %d, want %d", got, DefaultWindowLimit)
	}
	if DefaultWindowLimit != 120 {
		t.Fatalf("DefaultWindowLimit = %d, want the client WINDOW of 120", DefaultWindowLimit)
	}
}
