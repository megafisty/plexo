package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"plexo/internal/broker"
	"plexo/internal/config"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/store"
)

// Dialer opens a new upstream connection. Tests and the fake server provide
// in-memory connections; `cmd/plexo` wires the production WebSocket dialer
// (`fchat.DialConfig`).
type Dialer func(ctx context.Context) (fchat.Conn, error)

// Config configures a character session.
type Config struct {
	Character     string
	Account       string
	ClientName    string
	ClientVersion string
	Dial          Dialer
	Tickets       fchat.TicketManager
	Store         store.Store
	Broker        *broker.Broker
	Renderer      model.Renderer
	Logger        *slog.Logger
	RetryDelay    time.Duration

	// Settings is the character's persisted configuration (highlights,
	// auto-join, and future per-character fields). It is owned by the actor and
	// replaced whole through SetSettings, so a new field needs no plumbing here.
	Settings config.Character

	// OnStale is invoked on the session actor after the self NLN and on every
	// received PIN. The core uses it to refresh core-wide catalogs (official
	// channels, public rooms) when they are out of date.
	OnStale func(*Session)

	// OnCatalog receives CHA/ORS replies for core-wide catalog maintenance.
	// Exactly one of the two slices is non-nil per call.
	OnCatalog func(character string, official []model.OfficialChannel, rooms []model.PublicRoom)
}

// Session is one logged-in character. All state is owned by a single actor
// goroutine; callers interact through request channels.
type Session struct {
	cfg Config

	inbox chan any
	out   chan outbound // set by the active connection; actor-only

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool

	st  *state
	ads *adBuffer

	// settings is the resolved character configuration, owned by the actor. It
	// is read in recordEntry (highlights), autoJoin, and future per-character
	// logic, and replaced whole through SetSettings.
	settings config.Character

	// highlights is derived from settings and owned by the actor: read in
	// recordEntry and swapped only through SetSettings, both on the actor
	// goroutine.
	highlights highlighter

	// delivery renders raw BBCode to wire-ready payloads. The renderer is fixed
	// for the session's life, so it is built once and reused.
	delivery model.Delivery

	// gen counts serve() invocations. netInput carries the generation of the
	// connection it came from so stale input from a previous connection's
	// reader cannot kill the current one.
	gen int
}

// New creates a session. Call Start to run it.
func New(cfg Config) *Session {
	if cfg.ClientName == "" {
		cfg.ClientName = "Plexo"
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "0.1"
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 250 * time.Millisecond
	}
	s := &Session{
		cfg:        cfg,
		inbox:      make(chan any, 256),
		st:         newState(cfg.Character),
		ads:        newAdBuffer(defaultAdCapacity),
		settings:   cfg.Settings,
		highlights: newHighlighter(cfg.Settings.Highlights),
		delivery:   model.NewDelivery(cfg.Renderer),
	}
	return s
}

// Character returns the session's character name.
func (s *Session) Character() string { return s.cfg.Character }

// Ads returns the session's buffered LRP advertisements, oldest first. Ads are
// ephemeral and delivered on demand (the UI's ad tab/search), not pushed.
func (s *Session) Ads() []model.Ad { return s.ads.list() }

// AdCount returns the number of buffered ads.
func (s *Session) AdCount() int { return s.ads.len() }

// Start launches the actor.
func (s *Session) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Cancelling on exit marks the actor as no longer running, so callers
		// that wait on a reply return immediately instead of blocking forever
		// after a terminal disconnect.
		defer s.cancel()
		s.run(s.ctx)
	}()
}

// Stop cancels the actor and waits for it to exit.
func (s *Session) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// --- actor inputs ---

type netInput struct {
	gen int // connection generation; stale inputs are discarded
	cmd fchat.Frame
	err error
}

// sentInput reports that an outbound frame was written to the socket. It is
// posted by the writer and handled on the actor goroutine so durable state
// (a recorded self-message, an optimistic status) is only created after the
// frame actually left the process.
type sentInput struct {
	gen    int
	onSent func()
}

// outbound is one queued frame. onSent, when non-nil, runs on the actor after
// a successful write; a frame that never reaches the socket must not leave
// durable state behind.
type outbound struct {
	wire   fchat.Frame
	onSent func()
}

// call is one request to the session actor. run executes on the actor
// goroutine with exclusive access to session state and writes its result to a
// reply channel captured in its closure.
type call struct {
	run func()
}

// ask runs run on the session actor and waits for its reply. ok is false when
// the session is not running (never started, stopped, or disconnected), in
// which case the zero value is returned. It is the single request/response
// path for every actor input that produces a result.
func ask[T any](s *Session, run func(reply chan T)) (T, bool) {
	reply := make(chan T, 1)
	if !s.request(call{run: func() { run(reply) }}) {
		var zero T
		return zero, false
	}
	select {
	case v := <-reply:
		return v, true
	case <-s.done():
		var zero T
		return zero, false
	}
}

// ConvMeta is a session-owned view of a conversation, used to build
// materializations.
type ConvMeta struct {
	Ref         model.ConvRef
	Title       string
	Description string
	Mode        string
	Joined      bool
	Exists      bool
	Members     []model.MemberInfo
	// Role is the session's room-scoped authority in this conversation.
	Role model.RoomRole
}

// Send dispatches a command to the session. It returns the synchronous result.
func (s *Session) Send(cmd model.Command) model.Result {
	r, ok := ask(s, func(reply chan model.Result) { reply <- s.handleCommand(cmd) })
	if !ok {
		return model.Result{CID: cmd.CID, Accepted: false, ErrorCode: "not_running", ErrorMsg: "session not running"}
	}
	return r
}

// Snapshot returns the client-facing session state.
func (s *Session) Snapshot() model.SessionSnapshot {
	r, ok := ask(s, func(reply chan model.SessionSnapshot) { reply <- s.snapshotLocked() })
	if !ok {
		return model.SessionSnapshot{Character: s.cfg.Character}
	}
	return r
}

// ConvMeta returns session-owned metadata for a conversation.
func (s *Session) ConvMeta(conv model.ConvRef) (ConvMeta, bool) {
	r, ok := ask(s, func(reply chan ConvMeta) { reply <- s.convMetaLocked(conv) })
	if !ok {
		return ConvMeta{}, false
	}
	return r, r.Exists
}

// RoomInfo returns the on-demand management view of one conversation. The bool
// is false when the conversation is unknown or not joined. It is served over
// HTTP and never streams as conversation state.
func (s *Session) RoomInfo(conv model.ConvRef) (model.RoomInfo, bool) {
	type result struct {
		info   model.RoomInfo
		exists bool
	}
	r, ok := ask(s, func(reply chan result) {
		info, exists := s.roomInfoLocked(conv)
		reply <- result{info: info, exists: exists}
	})
	if !ok {
		return model.RoomInfo{}, false
	}
	return r.info, r.exists
}

// SearchPresence returns online roster entries matching the query. It is served
// on demand because live presence delivery is scoped to watched characters.
func (s *Session) SearchPresence(q model.PresenceQuery) []model.MemberInfo {
	r, _ := ask(s, func(reply chan []model.MemberInfo) { reply <- s.searchPresenceLocked(q) })
	return r
}

// Search queues an FKS on this session. It reports acceptance only; the result
// is cached on the session when it arrives and announced as a search event, to
// be pulled via SearchResults. The Result's CID is unused on this HTTP-
// triggered path.
func (s *Session) Search(q model.SearchQuery) model.Result {
	r, ok := ask(s, func(reply chan model.Result) { reply <- s.handleSearch(q) })
	if !ok {
		return model.Result{Accepted: false, ErrorCode: "not_running", ErrorMsg: "session not running"}
	}
	return r
}

// SetSettings replaces the session's resolved character configuration. It is
// safe to call while the session is running: the whole document and the derived
// highlighter are swapped on the actor goroutine, so an in-flight render never
// sees a half-updated config. A stopped session is a no-op.
func (s *Session) SetSettings(settings config.Character) {
	ask(s, func(reply chan struct{}) {
		s.settings = settings
		s.highlights = newHighlighter(settings.Highlights)
		reply <- struct{}{}
	})
}

func (s *Session) request(v any) bool {
	s.mu.Lock()
	ctx, started := s.ctx, s.started
	s.mu.Unlock()
	if !started || ctx == nil {
		return false
	}
	select {
	case s.inbox <- v:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Session) done() <-chan struct{} {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return ctx.Done()
}

func (s *Session) now() time.Time { return time.Now() }

func (s *Session) log() *slog.Logger {
	if s.cfg.Logger != nil {
		return s.cfg.Logger
	}
	return slog.Default()
}

// --- lifecycle ---

func (s *Session) run(ctx context.Context) {
	attempts := 0
	for {
		err := s.serve(ctx)
		// Typing is connection-scoped: clear it (and tell subscribers) as soon
		// as the connection ends, so a reconnect never shows stale indicators.
		s.clearTyping()
		// Search results are connection-scoped too: their presence came from
		// this connection's roster, so a reconnect must not serve them.
		s.clearSearch()
		if err == nil || ctx.Err() != nil {
			return // graceful stop
		}
		// wasLive reports whether the connection that just failed had reached
		// the ready state. Clear the phase so the next attempt is judged on its
		// own; a dial failure must not inherit the previous connection's liveness.
		wasLive := s.st.phase == "ready"
		s.st.phase = "idle"
		if wasLive {
			attempts = 0
		}
		reason, severity, auto := classify(err)
		s.invalidateTicket(err)
		s.log().Debug("session disconnected",
			"character", s.cfg.Character, "reason", reason, "auto", auto, "err", err)

		if auto && attempts == 0 {
			attempts++
			s.emitSessionState("connecting", reason, severity, true)
			select {
			case <-time.After(s.cfg.RetryDelay):
			case <-ctx.Done():
				return
			}
			continue
		}
		s.emitSessionState("disconnected", reason, severity, false)
		return
	}
}

func (s *Session) serve(ctx context.Context) error {
	if s.cfg.Dial == nil {
		return errors.New("session: no dialer configured")
	}
	conn, err := s.cfg.Dial(ctx)
	if err != nil {
		return fmt.Errorf("session: dial: %w", err)
	}
	defer conn.Close()

	ticket, err := s.cfg.Tickets.Ticket(ctx, s.cfg.Account)
	if err != nil {
		return fmt.Errorf("session: ticket: %w", err)
	}

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Bump the connection generation: inputs from older readers are stale.
	s.gen++
	gen := s.gen

	out := make(chan outbound, 128)
	s.out = out
	defer func() { s.out = nil }()

	// Reader: socket -> actor inbox.
	go func() {
		for {
			cmd, rerr := conn.Read(serveCtx)
			if rerr != nil {
				select {
				case s.inbox <- netInput{gen: gen, err: rerr}:
				case <-serveCtx.Done():
				}
				return
			}
			select {
			case s.inbox <- netInput{gen: gen, cmd: cmd}:
			case <-serveCtx.Done():
				return
			}
		}
	}()

	// Writer: actor queue -> socket. A write failure is delivered back to the
	// actor as an input error so the session tears down instead of queueing
	// into a channel nobody drains. Flood limits are the server's business;
	// oversends come back as ERR, which surfaces as a non-fatal error event.
	go s.writeLoop(serveCtx, conn, out, gen)

	s.st.phase = "idn_sent"
	s.st.conn = "connecting"
	s.emitSessionState("connecting", "", "", false)

	idn, err := fchat.New("IDN", fchat.IDNPayload{
		Method:    "ticket",
		Account:   s.cfg.Account,
		Ticket:    ticket.Value,
		Character: s.cfg.Character,
		CName:     s.cfg.ClientName,
		CVersion:  s.cfg.ClientVersion,
	})
	if err := conn.Write(serveCtx, idn); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case in := <-s.inbox:
			switch v := in.(type) {
			case netInput:
				if v.gen != gen {
					continue // stale input from a previous connection
				}
				if v.err != nil {
					return v.err
				}
				if herr := s.handle(v.cmd); herr != nil {
					return herr
				}
			case sentInput:
				if v.gen != gen {
					continue // frame from a previous connection
				}
				if v.onSent != nil {
					v.onSent()
				}
			case call:
				v.run()
			}
		}
	}
}

func (s *Session) writeLoop(ctx context.Context, conn fchat.Conn, out <-chan outbound, gen int) {
	for {
		select {
		case <-ctx.Done():
			return
		case ob := <-out:
			if err := conn.Write(ctx, ob.wire); err != nil {
				// A dead writer must not leave the actor queueing forever: report
				// the failure as connection input so serve returns and the session
				// either disconnects or auto-retries.
				select {
				case s.inbox <- netInput{gen: gen, err: err}:
				case <-ctx.Done():
				}
				return
			}
			if ob.onSent != nil {
				select {
				case s.inbox <- sentInput{gen: gen, onSent: ob.onSent}:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}
