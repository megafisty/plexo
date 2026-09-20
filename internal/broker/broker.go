// Package broker fans canonical events out to UI subscribers. It provides
// bounded, coalescing, demand-driven delivery: durable events are delivered or
// replaced by a fresh materialization, while latest-wins events coalesce.
package broker

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"plexo/internal/model"
)

// ViewBuilder materializes a conversation for a subscriber. It is implemented
// by the core manager (session state + store) and injected here to avoid a
// dependency cycle. A non-zero since requests a delta catch-up: only entries
// after that conv_seq, which the client merges into its retained window.
type ViewBuilder interface {
	ConvView(ctx context.Context, session string, conv model.ConvRef, limit int, since uint64) (model.ConvView, error)
}

// SubOpts configures a subscription.
type SubOpts struct {
	Buffer          int            // max queued batches before lag
	MaxBatch        int            // max events per batch
	FlushEvery      time.Duration  // coalescing tick
	DefaultInterest model.Interest // interest for conversations not explicitly set
	WindowLimit     int            // entries included in a materialization
}

// DefaultWindowLimit caps entries in a materialization. It intentionally
// matches the client's retained timeline window (ui/src/store/state.ts WINDOW),
// so a view never ships entries the client immediately trims away.
const DefaultWindowLimit = 120

// DefaultSubOpts returns sensible defaults.
func DefaultSubOpts() SubOpts {
	return SubOpts{
		Buffer:          256,
		MaxBatch:        64,
		FlushEvery:      25 * time.Millisecond,
		DefaultInterest: model.InterestSummary,
		WindowLimit:     DefaultWindowLimit,
	}
}

// Batch is one delivery to a subscriber. Events are ordered; a consumer that
// falls behind is resynced by key from the broker's state store rather than by
// streaming sequence numbers.
type Batch struct {
	Events []model.Event `json:"events"`
}

// Broker owns the subscription registry.
type Broker struct {
	mu      sync.Mutex
	subs    map[uint64]*Subscription
	nextID  uint64
	builder ViewBuilder
	closed  bool
	logger  *slog.Logger

	// friends and ignores are the account-wide watch sets. One account per
	// core, so they are global rather than per-session. Two spellings of the same
	// character must not be tracked twice.
	// They are stored as immutable pointers so Publish stays lock-free.
	friends atomic.Pointer[map[string]struct{}]
	ignores atomic.Pointer[map[string]struct{}]
	// friendSig fingerprints the last friends payload including inline presence,
	// so a presence refresh forwards while a duplicate reporter is suppressed.
	friendSig atomic.Pointer[string]

	// memMu guards convMembers and its reverse index charConvs. Together they
	// are the single authoritative source for presence scoping: membership is
	// account-wide within a session, so it is not mirrored per subscription.
	// They change only on conversation events, so they are off the hot delivery
	// path. charConvs (session -> lowercased character -> conv keys) makes
	// presence scoping proportional to the conversations that contain the
	// character rather than every conversation in the session.
	memMu       sync.RWMutex
	convMembers map[string]map[string]convMembership
	charConvs   map[string]map[string]map[string]struct{}

	// states holds the latest value per state key, one copy shared by every
	// subscription. It is the source of truth for keyed resync: a subscriber that
	// missed a state update re-reads it here instead of rebuilding a snapshot.
	// Typing is deliberately not stored (ephemeral, no resync value).
	states sync.Map // key string -> storedState

	// snapshot is an immutable view of subs for lock-free Publish. It is
	// rebuilt under mu whenever the registry changes.
	snapshot atomic.Pointer[[]*Subscription]
}

// storedState is one state record plus the session that reported it, so a
// resync can route it through the same interest/watch gating as the original.
type storedState struct {
	session string
	payload model.StatePayload
}

// convMembership is one conversation's lowercased member set plus the ref
// needed to resolve the subscriber's interest in it.
type convMembership struct {
	conv    model.ConvRef
	members map[string]struct{}
}

// New returns an empty broker.
func New() *Broker {
	b := &Broker{
		subs:        map[uint64]*Subscription{},
		logger:      slog.Default(),
		convMembers: map[string]map[string]convMembership{},
		charConvs:   map[string]map[string]map[string]struct{}{},
	}
	empty := map[string]struct{}{}
	b.friends.Store(&empty)
	b.ignores.Store(&empty)
	emptySig := ""
	b.friendSig.Store(&emptySig)
	return b
}

// SetLogger overrides the broker's logger (nil restores the default).
func (b *Broker) SetLogger(l *slog.Logger) {
	b.mu.Lock()
	b.logger = l
	b.mu.Unlock()
}

func (b *Broker) log() *slog.Logger {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// refreshSnapshotLocked rebuilds the lock-free publish snapshot. Callers must
// hold b.mu.
func (b *Broker) refreshSnapshotLocked() {
	snap := make([]*Subscription, 0, len(b.subs))
	for _, s := range b.subs {
		snap = append(snap, s)
	}
	b.snapshot.Store(&snap)
}

// getBuilder returns the current view builder under the lock, so reading it
// from a subscription goroutine races with neither SetViewBuilder nor Close.
func (b *Broker) getBuilder() ViewBuilder {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder
}

// SetViewBuilder injects the materialization implementation.
func (b *Broker) SetViewBuilder(vb ViewBuilder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.builder = vb
}

// Subscribe registers a new subscriber and starts its delivery goroutine.
func (b *Broker) Subscribe(opts SubOpts) *Subscription {
	if opts.Buffer <= 0 {
		opts.Buffer = 256
	}
	if opts.MaxBatch <= 0 {
		opts.MaxBatch = 64
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = 25 * time.Millisecond
	}
	if opts.DefaultInterest == "" {
		opts.DefaultInterest = model.InterestSummary
	}
	if opts.WindowLimit <= 0 {
		opts.WindowLimit = DefaultWindowLimit
	}

	b.mu.Lock()
	b.nextID++
	id := b.nextID
	s := &Subscription{
		id:       id,
		broker:   b,
		opts:     opts,
		policy:   newDeliveryPolicy(b, opts),
		msg:      make(chan subMsg, 1024),
		out:      make(chan Batch, opts.Buffer),
		done:     make(chan struct{}),
		gen:      map[interestKey]uint64{},
		maturing: map[interestKey]uint64{},
		backlog:  map[string][]model.Event{},
		bview:    make(chan builtView, 64),
	}
	b.subs[id] = s
	b.refreshSnapshotLocked()
	b.mu.Unlock()

	go s.run()
	return s
}

// Publish delivers an event to all subscribers. It reads the immutable
// subscriber snapshot, so it takes no lock and allocates no slice per event.
// State records are stored once (last value wins) and account-scoped sets are
// de-duplicated: every session reports the same FRL/IGN, so only a real change
// is forwarded.
func (b *Broker) Publish(ev model.Event) {
	if ev.Kind == model.EvState {
		sp, ok := ev.Payload.(model.StatePayload)
		if !ok || sp.Key == "" {
			return
		}
		switch model.KeyNamespace(sp.Key) {
		case model.StateAccount:
			if !b.setAccount(sp) {
				return
			}
		case model.StateConv:
			b.applyConvState(ev.Session, sp)
		}
		if model.KeyNamespace(sp.Key) != model.StateTyping {
			b.states.Store(sp.Key, storedState{session: ev.Session, payload: sp})
		}
	}
	snap := b.snapshot.Load()
	if snap == nil {
		return
	}
	for _, s := range *snap {
		s.push(subMsg{ev: &ev})
	}
}

// state returns the latest stored value for a key.
func (b *Broker) state(key string) (storedState, bool) {
	v, ok := b.states.Load(key)
	if !ok {
		return storedState{}, false
	}
	return v.(storedState), true
}

// rangeStates visits every stored state record. A subscriber that panicked
// mid-segment uses this for a broad resync.
func (b *Broker) rangeStates(fn func(storedState)) {
	b.states.Range(func(_, v any) bool {
		fn(v.(storedState))
		return true
	})
}

// DropSession forgets everything the broker stored for one character: its
// membership indexes and every state key under its namespace. The manager calls
// it on logout so a later re-login cannot inherit tombstones or stale presence.
func (b *Broker) DropSession(character string) {
	if character == "" {
		return
	}
	sessKey := model.SessionKey(character)
	prefixes := []string{
		model.StateConv + "/" + character + "/",
		model.StateSummary + "/" + character + "/",
		model.StateTyping + "/" + character + "/",
		model.StateSearch + "/" + character,
		model.StateInvites + "/" + character,
	}
	b.states.Range(func(k, _ any) bool {
		key := k.(string)
		if key == sessKey {
			b.states.Delete(key)
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				b.states.Delete(key)
				break
			}
		}
		return true
	})
	b.memMu.Lock()
	// Presence is watched only via a friend or a full conversation's membership.
	// Record the names this session's conversations referenced, drop its indexes,
	// and note which names any remaining session still references.
	dropped := map[string]struct{}{strings.ToLower(character): {}}
	for name := range b.charConvs[character] {
		dropped[name] = struct{}{}
	}
	delete(b.convMembers, character)
	delete(b.charConvs, character)
	stillReferenced := map[string]struct{}{}
	for _, chars := range b.charConvs {
		for name := range chars {
			stillReferenced[name] = struct{}{}
		}
	}
	b.memMu.Unlock()

	// Drop presence records that no remaining session can watch. Without this,
	// character/<name> keys accumulate for the process lifetime and bloat every
	// later broad resync. Friends stay: they are watched account-wide.
	b.states.Range(func(k, _ any) bool {
		key, _ := k.(string)
		if !strings.HasPrefix(key, model.StateCharacter+"/") {
			return true
		}
		lower := strings.ToLower(strings.TrimPrefix(key, model.StateCharacter+"/"))
		if _, ok := dropped[lower]; !ok {
			return true
		}
		if _, ok := stillReferenced[lower]; ok {
			return true
		}
		if b.isFriend(lower) {
			return true
		}
		b.states.Delete(key)
		return true
	})
}

// applyConvState updates the broker-owned membership from a conversation state
// record. It runs for every publish, subscriber or not, so a late subscriber
// inherits the current membership. A DM carries no member list, so its partner
// is implied by the conversation id.
func (b *Broker) applyConvState(session string, sp model.StatePayload) {
	b.memMu.Lock()
	defer b.memMu.Unlock()
	if sp.Removed {
		key := strings.TrimPrefix(sp.Key, model.StateConv+"/"+session+"/")
		b.dropMembershipLocked(session, key)
		return
	}
	p, ok := sp.Value.(model.ConvStatePayload)
	if !ok {
		return
	}
	conv, ok := model.ConvRefFromKey(sp.Key)
	if !ok {
		return
	}
	members := map[string]struct{}{}
	if conv.Kind == model.ConvDM {
		members[strings.ToLower(conv.ID)] = struct{}{}
	} else {
		for _, m := range p.Members {
			if m != "" {
				members[strings.ToLower(m)] = struct{}{}
			}
		}
	}
	b.setMembershipLocked(session, convMembership{conv: conv, members: members})
}

// seedConvMembers replaces a conversation's membership from a materialized
// view. The view is authoritative for implied members (a DM's partner) that a
// bare ConvPayload omits.
func (b *Broker) seedConvMembers(session string, conv model.ConvRef, members []string) {
	b.memMu.Lock()
	defer b.memMu.Unlock()
	next := make(map[string]struct{}, len(members))
	for _, m := range members {
		if m != "" {
			next[strings.ToLower(m)] = struct{}{}
		}
	}
	b.setMembershipLocked(session, convMembership{conv: conv, members: next})
}

// setMembershipLocked stores one conversation's membership and keeps the
// reverse character index in step. Callers hold memMu.
func (b *Broker) setMembershipLocked(session string, cm convMembership) {
	key := cm.conv.Key()
	bySess := b.convMembers[session]
	if bySess == nil {
		bySess = map[string]convMembership{}
		b.convMembers[session] = bySess
	}
	if old, ok := bySess[key]; ok {
		b.unindexLocked(session, key, old.members)
	}
	bySess[key] = cm
	chars := b.charConvs[session]
	if chars == nil {
		chars = map[string]map[string]struct{}{}
		b.charConvs[session] = chars
	}
	for name := range cm.members {
		set := chars[name]
		if set == nil {
			set = map[string]struct{}{}
			chars[name] = set
		}
		set[key] = struct{}{}
	}
}

// dropMembershipLocked removes one conversation and its reverse-index entries.
// Callers hold memMu.
func (b *Broker) dropMembershipLocked(session, key string) {
	bySess := b.convMembers[session]
	if bySess == nil {
		return
	}
	if old, ok := bySess[key]; ok {
		b.unindexLocked(session, key, old.members)
		delete(bySess, key)
	}
	if len(bySess) == 0 {
		delete(b.convMembers, session)
	}
}

// unindexLocked removes one conversation's character associations from the
// reverse index. Callers hold memMu.
func (b *Broker) unindexLocked(session, key string, members map[string]struct{}) {
	chars := b.charConvs[session]
	if chars == nil {
		return
	}
	for name := range members {
		set := chars[name]
		if set == nil {
			continue
		}
		delete(set, key)
		if len(set) == 0 {
			delete(chars, name)
		}
	}
	if len(chars) == 0 {
		delete(b.charConvs, session)
	}
}

// watchConvs reports whether character is a member of any conversation the
// full predicate accepts. Membership is read under memMu; the predicate is
// called while holding it (lock order memMu -> policy.mu, never the reverse).
// The reverse index scans only the conversations that contain the character,
// instead of every conversation in the session.
func (b *Broker) watchConvs(session, character string, full func(model.ConvRef) bool) bool {
	b.memMu.RLock()
	defer b.memMu.RUnlock()
	chars := b.charConvs[session]
	if chars == nil {
		return false
	}
	convs := chars[character]
	if len(convs) == 0 {
		return false
	}
	bySess := b.convMembers[session]
	for key := range convs {
		cm, ok := bySess[key]
		if !ok {
			continue
		}
		if full(cm.conv) {
			return true
		}
	}
	return false
}

// SetAccountFriends replaces the broker's full friend/bookmark watch set. The
// session reports the account union here, independently of the filtered friends
// payload it streams to clients: the client only needs online friends, but the
// broker must watch offline friends too so their later return is delivered.
func (b *Broker) SetAccountFriends(names []string) {
	next := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			next[strings.ToLower(name)] = struct{}{}
		}
	}
	if sameSet(b.friends.Load(), next) {
		return
	}
	b.friends.Store(&next)
}

// setAccount replaces the broker-owned account-wide set for an account-scoped
// event and reports whether it changed, so a duplicate reporter does not fan
// out a second copy. Friends and ignores are set-to. The friends watch set
// itself is supplied separately through SetAccountFriends; this only
// de-duplicates the client-facing projection by its lowercased name set.
func (b *Broker) setAccount(sp model.StatePayload) bool {
	switch p := sp.Value.(type) {
	case model.FriendsPayload:
		sig := friendSetSig(p.Friends)
		cur := ""
		if s := b.friendSig.Load(); s != nil {
			cur = *s
		}
		if sig == cur {
			return false
		}
		b.friendSig.Store(&sig)
		return true
	case model.IgnoresPayload:
		next := make(map[string]struct{}, len(p.Ignores))
		for _, name := range p.Ignores {
			if name != "" {
				next[strings.ToLower(name)] = struct{}{}
			}
		}
		if sameSet(b.ignores.Load(), next) {
			return false
		}
		b.ignores.Store(&next)
		return true
	default:
		return true
	}
}

// friendSetSig fingerprints a friends payload by its lowercased name set. A
// report that only refreshes inline presence is therefore suppressed: presence
// streams separately as presence events, so re-fanning the whole list on every
// friend status change is wasted work.
func friendSetSig(friends []model.MemberInfo) string {
	rows := make([]string, 0, len(friends))
	for _, f := range friends {
		if f.Name == "" {
			continue
		}
		rows = append(rows, strings.ToLower(f.Name))
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// sameSet reports whether two immutable name sets are equal.
func sameSet(cur *map[string]struct{}, next map[string]struct{}) bool {
	if cur == nil {
		return len(next) == 0
	}
	m := *cur
	if len(m) != len(next) {
		return false
	}
	for k := range m {
		if _, ok := next[k]; !ok {
			return false
		}
	}
	return true
}

// isFriend reports whether name is an account friend/bookmark. It reads the
// immutable snapshot, so it never takes the broker lock.
func (b *Broker) isFriend(name string) bool {
	m := b.friends.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[strings.ToLower(name)]
	return ok
}

// Close stops all subscriptions.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*Subscription, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = map[uint64]*Subscription{}
	b.refreshSnapshotLocked()
	b.mu.Unlock()
	// Close outside the broker lock: Subscription.Close takes it too.
	for _, s := range subs {
		s.Close()
	}
}

type control struct {
	session string
	conv    model.ConvRef
	level   model.Interest
	// since is the client's highest seen conv_seq on a full-interest re-assert,
	// or 0 for a full materialization. See ViewBuilder.ConvView.
	since uint64
}

type subMsg struct {
	ev      *model.Event
	control *control
}

// interestKey identifies one conversation within one session. A struct key
// avoids building a composite string on every delivered event.
type interestKey struct {
	session string
	conv    model.ConvRef
}

// maxViewBacklog bounds the events held for one session while its conversation
// views materialize. Beyond it the event is marked for resync and delivered
// directly, so a stuck build cannot grow memory without bound.
const maxViewBacklog = 1024

// errViewPanic marks a materialization that panicked in its build goroutine.
var errViewPanic = errors.New("view build panicked")

// builtView is one completed (or failed) conversation materialization, handed
// back to the delivery loop so it can emit the view before the events held
// during its build.
type builtView struct {
	session string
	conv    model.ConvRef
	gen     uint64
	view    model.ConvView
	err     error
}

// Subscription is one consumer's view of the event stream. It owns only the
// transport-neutral fan-out state; F-Chat delivery semantics live in its policy.
type Subscription struct {
	id     uint64
	broker *Broker
	opts   SubOpts
	policy *deliveryPolicy
	msg    chan subMsg
	out    chan Batch
	done   chan struct{}
	once   sync.Once

	// Materialization state is touched only by the delivery loop. gen versions a
	// conversation's builds so a superseded result is dropped; maturing records
	// the in-flight builds; backlog holds a session's deliverable events while
	// any of its views is in flight, so a view always precedes the events that
	// raced its build; bview receives completed builds.
	gen      map[interestKey]uint64
	maturing map[interestKey]uint64
	backlog  map[string][]model.Event
	bview    chan builtView

	mu         sync.Mutex
	dirtyKeys  map[string]struct{}
	dirtyConvs map[interestKey]struct{}
	resyncAll  bool
}

// Events returns the delivery channel. It closes when the subscription closes.
func (s *Subscription) Events() <-chan Batch { return s.out }

// TakeDirty reports and clears the pending resync set. It is not used by the
// delivery loop (which drains it through resyncDirty); it exists for tests.
func (s *Subscription) TakeDirty() (keys []string, all bool) {
	k, _, all := s.takeDirty()
	return k, all
}

// markDirty records that the subscriber may have missed one event, so the next
// tick re-sends the latest value for its state key (or re-materializes its
// conversation). Typing is ephemeral and skipped: a re-sent typing value would
// be stale anyway.
func (s *Subscription) markDirty(ev model.Event) {
	switch ev.Kind {
	case model.EvState:
		p, ok := ev.Payload.(model.StatePayload)
		if !ok || p.Key == "" || model.KeyNamespace(p.Key) == model.StateTyping {
			return
		}
		s.mu.Lock()
		if s.dirtyKeys == nil {
			s.dirtyKeys = map[string]struct{}{}
		}
		s.dirtyKeys[p.Key] = struct{}{}
		s.mu.Unlock()
	case model.EvMessage:
		p, ok := ev.Payload.(model.MessagePayload)
		if !ok {
			return
		}
		s.mu.Lock()
		if s.dirtyConvs == nil {
			s.dirtyConvs = map[interestKey]struct{}{}
		}
		s.dirtyConvs[interestKey{session: ev.Session, conv: p.Conv}] = struct{}{}
		s.mu.Unlock()
	}
}

// markResyncAll requests a broad resync after a dropped worker segment, where
// the loop cannot know which individual events were lost.
func (s *Subscription) markResyncAll() {
	s.mu.Lock()
	s.resyncAll = true
	s.mu.Unlock()
}

// MarkDirtyBatch records that a consumer failed to take a delivered batch, so
// the next tick re-emits the latest value per state key (and re-materializes
// any conversation whose stream entry was dropped). Stream and state events are
// handled; views and errors carry no resync identity.
func (s *Subscription) MarkDirtyBatch(events []model.Event) {
	for _, ev := range events {
		s.markDirty(ev)
	}
}

// takeDirty atomically drains the pending resync set.
func (s *Subscription) takeDirty() (keys []string, convs []interestKey, all bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all = s.resyncAll
	s.resyncAll = false
	for k := range s.dirtyKeys {
		keys = append(keys, k)
	}
	for k := range s.dirtyConvs {
		convs = append(convs, k)
	}
	s.dirtyKeys = nil
	s.dirtyConvs = nil
	return keys, convs, all
}

// resyncDirty re-emits the latest value for keys the subscriber missed and
// re-materializes conversations whose stream gap cannot be reconstructed. It
// runs on the delivery goroutine, so startView may touch loop-only state.
func (s *Subscription) resyncDirty(ctx context.Context, enqueue func(model.Event)) {
	keys, convs, all := s.takeDirty()
	if all {
		s.broker.rangeStates(func(st storedState) {
			ev := model.Event{Session: st.session, Kind: model.EvState, Payload: st.payload}
			if s.policy.deliver(ev) {
				enqueue(ev)
			}
		})
		for _, key := range s.policy.fullConvs() {
			s.startView(ctx, key.session, key.conv, 0)
		}
	}
	for _, k := range keys {
		st, ok := s.broker.state(k)
		if !ok {
			continue
		}
		enqueue(model.Event{Session: st.session, Kind: model.EvState, Payload: st.payload})
	}
	for _, key := range convs {
		s.startView(ctx, key.session, key.conv, 0)
	}
}

// SetInterest changes the interest level for a conversation. Enabling full
// interest triggers a materialization, delivered in stream order; a non-zero
// since makes it a delta catch-up for a client that still holds the window.
// The send blocks so an interest change is never silently dropped: the run loop
// consumes this channel promptly, and its only slow operation (a ConvView
// build) is bounded.
func (s *Subscription) SetInterest(session string, conv model.ConvRef, level model.Interest, since uint64) {
	select {
	case s.msg <- subMsg{control: &control{session: session, conv: conv, level: level, since: since}}:
	case <-s.done:
	}
}

// Close stops delivery.
func (s *Subscription) Close() {
	s.once.Do(func() { close(s.done) })
	s.broker.mu.Lock()
	delete(s.broker.subs, s.id)
	s.broker.refreshSnapshotLocked()
	s.broker.mu.Unlock()
}

func (s *Subscription) push(m subMsg) {
	select {
	case s.msg <- m:
	case <-s.done:
	default:
		if m.ev != nil {
			s.markDirty(*m.ev)
		}
	}
}

func (s *Subscription) run() {
	defer close(s.out)

	// A cancellation context so an in-flight view build (actor round-trip +
	// store reads) is dropped when the subscriber closes.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		if s.serve(ctx) {
			return
		}
	}
}

// serve is one delivery-loop segment. It returns true when the subscription is
// closed. A panic (payload handling) is logged and marks the subscriber lagged
// so the consumer resyncs; the loop is restarted rather than silently killing
// delivery. View builds run in their own goroutines, so a slow session can no
// longer stall the delivery loop.
func (s *Subscription) serve(ctx context.Context) (closed bool) {
	defer func() {
		if r := recover(); r != nil {
			s.broker.log().Error("broker: subscription worker panic", "panic", r)
			s.markResyncAll()
		}
	}()

	tick := time.NewTicker(s.opts.FlushEvery)
	defer tick.Stop()

	var pending []model.Event
	index := map[string]int{}

	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := Batch{Events: pending}
		pending = nil
		index = map[string]int{}
		select {
		case s.out <- batch:
		case <-s.done:
		default:
			// Consumer is not keeping up; remember exactly what was dropped so the
			// tick re-emits the latest value per key (or re-materializes a conv).
			for _, ev := range batch.Events {
				s.markDirty(ev)
			}
		}
	}

	enqueue := func(ev model.Event) {
		if key := ev.CoalesceKey(); key != "" {
			if idx, ok := index[key]; ok {
				pending[idx] = ev
				return
			}
			index[key] = len(pending)
		}
		pending = append(pending, ev)
	}

	for {
		select {
		case <-s.done:
			flush()
			return true
		case bv := <-s.bview:
			s.finishView(bv, enqueue, flush)
		case m := <-s.msg:
			if m.control != nil {
				c := m.control
				s.policy.setInterest(c.session, c.conv, c.level)
				if c.level == model.InterestFull {
					s.startView(ctx, c.session, c.conv, c.since)
				} else {
					s.cancelView(c.session, c.conv)
					s.flushBacklog(c.session, enqueue)
				}
				continue
			}
			ev := *m.ev
			if !s.policy.deliver(ev) {
				continue
			}
			if s.holdEvent(ev) {
				continue
			}
			enqueue(ev)
			if len(pending) >= s.opts.MaxBatch {
				flush()
			}
		case <-tick.C:
			s.resyncDirty(ctx, enqueue)
			flush()
		}
	}
}

// startView begins a materialization for one conversation off the delivery
// loop. since selects a delta catch-up (0 is a full window). The result comes
// back on bview; a newer request for the same conversation supersedes the older
// one. With no builder there is nothing to build, and no backlog is held.
func (s *Subscription) startView(ctx context.Context, session string, conv model.ConvRef, since uint64) {
	builder := s.broker.getBuilder()
	if builder == nil {
		return
	}
	key := interestKey{session, conv}
	gen := s.gen[key] + 1
	s.gen[key] = gen
	s.maturing[key] = gen
	go s.buildView(ctx, key, gen, builder, since)
}

// cancelView invalidates any in-flight build for a conversation whose interest
// was downgraded, so a stale materialization is not delivered.
func (s *Subscription) cancelView(session string, conv model.ConvRef) {
	key := interestKey{session, conv}
	if _, ok := s.maturing[key]; !ok {
		return
	}
	delete(s.maturing, key)
	s.gen[key]++
}

// buildView runs one materialization. A panic is recovered here so it cannot
// take down the subscription; it is reported as a failed build and marks the
// subscriber lagged for a resync.
func (s *Subscription) buildView(ctx context.Context, key interestKey, gen uint64, builder ViewBuilder, since uint64) {
	bv := builtView{session: key.session, conv: key.conv, gen: gen}
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.broker.log().Error("broker: conversation view panic", "panic", r)
				bv.err = errViewPanic
			}
		}()
		bv.view, bv.err = builder.ConvView(ctx, key.session, key.conv, s.opts.WindowLimit, since)
	}()
	s.postBuilt(bv)
}

func (s *Subscription) postBuilt(bv builtView) {
	select {
	case s.bview <- bv:
	case <-s.done:
	}
}

// finishView emits a completed materialization followed by the events held
// during its build, then flushes. A superseded result is discarded.
func (s *Subscription) finishView(bv builtView, enqueue func(model.Event), flush func()) {
	key := interestKey{bv.session, bv.conv}
	gen, ok := s.maturing[key]
	if !ok || gen != bv.gen || s.gen[key] != bv.gen {
		return
	}
	delete(s.maturing, key)
	if bv.err != nil {
		// Surface the failure instead of leaving the subscriber waiting forever
		// for a view that will never arrive.
		s.broker.log().Warn("broker: conversation view failed",
			"session", bv.session, "conv", bv.conv.Key(), "err", bv.err)
		enqueue(model.Event{
			Session: bv.session,
			Kind:    model.EvError,
			Time:    time.Now(),
			Payload: model.ErrorPayload{Session: bv.session, Message: "could not load conversation"},
		})
	} else {
		members := make([]string, len(bv.view.Members))
		for i, m := range bv.view.Members {
			members[i] = m.Name
		}
		s.broker.seedConvMembers(bv.session, bv.conv, members)
		enqueue(model.Event{
			Session: bv.session,
			Kind:    model.EvConvView,
			Time:    time.Now(),
			Payload: bv.view,
		})
	}
	s.flushBacklog(bv.session, enqueue)
	flush()
}

// holdEvent diverts a deliverable event into its session's backlog while a
// view for that session is in flight, so the view is emitted before the events
// that raced its build. Reports whether the event was held.
func (s *Subscription) holdEvent(ev model.Event) bool {
	if len(s.maturing) == 0 || ev.Session == "" {
		return false
	}
	for key := range s.maturing {
		if key.session != ev.Session {
			continue
		}
		if len(s.backlog[ev.Session]) >= maxViewBacklog {
			// A build is taking too long; stop holding, remember the event for
			// resync, and deliver it directly so it is not lost.
			s.markDirty(ev)
			return false
		}
		s.backlog[ev.Session] = append(s.backlog[ev.Session], ev)
		return true
	}
	return false
}

// flushBacklog drains a session's held events once no view for it is in
// flight.
func (s *Subscription) flushBacklog(session string, enqueue func(model.Event)) {
	for key := range s.maturing {
		if key.session == session {
			return
		}
	}
	held := s.backlog[session]
	if len(held) == 0 {
		delete(s.backlog, session)
		return
	}
	delete(s.backlog, session)
	for _, ev := range held {
		enqueue(ev)
	}
}
