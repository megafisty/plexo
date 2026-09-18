package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"plexo/internal/broker"
	"plexo/internal/core"
	"plexo/internal/model"
)

// DefaultMaxClientMessage bounds a single inbound command from the browser.
const DefaultMaxClientMessage = 1 << 20 // 1 MiB

// writeTimeout bounds a single outbound write so a stuck client cannot pin a
// goroutine forever.
const writeTimeout = 10 * time.Second

// Bridge upgrades browser connections and bridges them to the core. It is the
// browser-facing counterpart of internal/fchat: the only place the downstream
// envelope is encoded.
type Bridge struct {
	manager *core.Manager
	account *core.Account
	auth    *SessionAuth
	logger  *slog.Logger

	// mu guards clients, the remote-address refcount of connected browser
	// sockets. It backs the dev console's client pane.
	mu      sync.Mutex
	clients map[string]int

	// subsMu guards subs, the durable subscriptions keyed by a client-generated
	// id. A subscription outlives the socket that created it: a drop detaches it
	// and starts a grace timer, and a reconnect with the same id reattaches
	// (preserving interest) until that timer fires.
	subsMu sync.Mutex
	subs   map[string]*subscription
	grace  time.Duration
}

// NewBridge wires a bridge to the core manager, account, and session gate.
func NewBridge(manager *core.Manager, account *core.Account, auth *SessionAuth, logger *slog.Logger) *Bridge {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bridge{
		manager: manager,
		account: account,
		auth:    auth,
		logger:  logger,
		clients: make(map[string]int),
		subs:    make(map[string]*subscription),
		grace:   DefaultSubscriptionGrace,
	}
}

// Clients returns the remote addresses of the connected browser WebSocket
// clients, sorted. One socket from an address is listed once; multiple sockets
// from the same address are listed once per socket.
func (b *Bridge) Clients() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.clients))
	for addr, n := range b.clients {
		for i := 0; i < n; i++ {
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

func (b *Bridge) addClient(addr string) {
	b.mu.Lock()
	b.clients[addr]++
	b.mu.Unlock()
}

func (b *Bridge) removeClient(addr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.clients[addr] <= 1 {
		delete(b.clients, addr)
		return
	}
	b.clients[addr]--
}

// subscription is a broker subscription owned by the bridge, keyed by a
// client-generated id so it can survive the socket that created it.
type subscription struct {
	sub      *broker.Subscription
	attached bool
	timer    *time.Timer
}

// resumeSubscription reattaches a socket to the detached subscription for id,
// preserving its interest. It reports false when there is no detached entry
// (never seen, or already expired, or still attached to a live socket).
func (b *Bridge) resumeSubscription(id string) (*broker.Subscription, bool) {
	b.subsMu.Lock()
	defer b.subsMu.Unlock()
	e, ok := b.subs[id]
	if !ok || e.attached {
		return nil, false
	}
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.attached = true
	return e.sub, true
}

// registerSubscription makes a fresh subscription durable under id. It reports
// false when id is already taken (another live socket owns it), which the
// caller treats as a bad subscribe rather than sharing one delivery channel
// between two readers.
func (b *Bridge) registerSubscription(id string, sub *broker.Subscription) bool {
	b.subsMu.Lock()
	defer b.subsMu.Unlock()
	if _, ok := b.subs[id]; ok {
		return false
	}
	b.subs[id] = &subscription{sub: sub, attached: true}
	return true
}

// releaseSubscription detaches a socket's subscription and starts the grace
// timer. A reconnect before it fires resumes the same subscription; otherwise
// it is closed and the next connect starts fresh.
func (b *Bridge) releaseSubscription(id string) {
	b.subsMu.Lock()
	defer b.subsMu.Unlock()
	e, ok := b.subs[id]
	if !ok || !e.attached {
		return
	}
	e.attached = false
	e.timer = time.AfterFunc(b.grace, func() { b.expireSubscription(id) })
}

// expireSubscription closes an expired, still-detached subscription. It
// rechecks attachment under the lock so a reconnect racing the timer wins.
func (b *Bridge) expireSubscription(id string) {
	b.subsMu.Lock()
	e, ok := b.subs[id]
	if !ok || e.attached {
		b.subsMu.Unlock()
		return
	}
	delete(b.subs, id)
	b.subsMu.Unlock()
	e.sub.Close()
}

// close stops the grace timers and closes every subscription. A live server
// holds its bridge for the process lifetime; tests call this to release it.
func (b *Bridge) close() {
	b.subsMu.Lock()
	subs := make([]*broker.Subscription, 0, len(b.subs))
	for id, e := range b.subs {
		if e.timer != nil {
			e.timer.Stop()
		}
		subs = append(subs, e.sub)
		delete(b.subs, id)
	}
	b.subsMu.Unlock()
	for _, sub := range subs {
		sub.Close()
	}
}

// Handler returns the http.Handler for the /ws endpoint.
func (b *Bridge) Handler() http.Handler { return http.HandlerFunc(b.serve) }

func (b *Bridge) serve(w http.ResponseWriter, r *http.Request) {
	if !b.auth.Authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// A nil AcceptOptions enforces same-origin: the Origin header must match the
	// request Host, which is also the check the LAN deployment wants.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		b.logger.Warn("ws: upgrade failed", "err", err)
		return
	}
	ws.SetReadLimit(DefaultMaxClientMessage)

	b.addClient(r.RemoteAddr)
	defer b.removeClient(r.RemoteAddr)

	c := &client{
		ws:     ws,
		bridge: b,
		send:   make(chan Envelope, 64),
		urgent: make(chan Envelope, 256),
		done:   make(chan struct{}),
	}
	c.run(r.Context())
}

// DefaultSubscriptionGrace bounds how long a detached subscription keeps its
// interest for a reconnecting socket. Too short and a slow blip loses interest;
// too long and a closed tab holds a subscriber and its buffered events.
const DefaultSubscriptionGrace = 5 * time.Second

// client is one browser connection.
type client struct {
	ws     *websocket.Conn
	bridge *Bridge
	send   chan Envelope
	// urgent carries acks/errors; writeLoop drains it before send so a slow
	// client loses a batch before it loses a command result. Overflow cancels
	// the connection: a result is never dropped silently.
	urgent chan Envelope
	done   chan struct{}
	sub    *broker.Subscription
	// subID is the durable subscription's client-generated id. It selects the
	// registry entry this socket detaches from on close.
	subID string

	// cancel tears the connection down. It is set by run before the worker
	// goroutines start and lets enqueueUrgent fail fast when a result cannot be
	// queued (a stuck writer would otherwise deadlock a blocking send).
	cancel context.CancelFunc
}

func (c *client) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.cancel = cancel
	defer close(c.done)
	defer c.ws.CloseNow()

	var wg sync.WaitGroup
	// Stop the worker goroutines before the subscription is closed.
	defer func() {
		cancel()
		wg.Wait()
	}()

	// The first envelope must be subscribe: it names the durable subscription
	// this socket attaches to. The UI is embedded in this binary, so there is no
	// older-client path to keep alive.
	first, ok := c.readEnvelope(ctx)
	if !ok {
		return
	}
	if first.Type != TSubscribe {
		c.sendErr(first.CID, "expected_subscribe", "the first envelope must be subscribe")
		return
	}
	var req Subscribe
	if err := json.Unmarshal(first.Data, &req); err != nil || req.ID == "" {
		c.sendErr("", "bad_subscribe", "subscribe id is required")
		return
	}

	resumed := false
	if sub, ok := c.bridge.resumeSubscription(req.ID); ok {
		c.sub = sub
		resumed = true
	} else {
		sub := c.bridge.manager.Subscribe()
		if !c.bridge.registerSubscription(req.ID, sub) {
			sub.Close()
			c.sendErr("", "subscription_in_use", "subscription id is already attached")
			return
		}
		c.sub = sub
	}
	c.subID = req.ID
	defer c.bridge.releaseSubscription(req.ID)

	wg.Add(1)
	go func() { defer wg.Done(); c.writeLoop(ctx) }()

	// The subscription is registered with the broker before the snapshot is
	// built, so no event can slip between the two. On a resume the client keeps
	// its state and the subscription keeps its interest, so there is no snapshot
	// and no re-materialization: only the batches buffered across the blip.
	c.enqueue(Envelope{Type: THello, Data: rawJSON(Hello{Version: ProtocolVersion, Resumed: resumed})})
	if !resumed {
		c.enqueue(Envelope{Type: TSnapshot, Data: rawJSON(c.bridge.manager.Snapshot())})
	}

	wg.Add(1)
	go func() { defer wg.Done(); c.forwardBatches(ctx) }()

	accountCh, cancelAccount := c.bridge.account.Subscribe()
	defer cancelAccount()
	wg.Add(1)
	go func() { defer wg.Done(); c.forwardAccount(ctx, accountCh) }()

	c.readLoop(ctx)
}

// readEnvelope reads one text envelope, skipping non-text frames and reporting
// (but skipping) malformed ones. It reports false when the socket closes.
func (c *client) readEnvelope(ctx context.Context) (Envelope, bool) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return Envelope{}, false
		}
		if typ != websocket.MessageText {
			continue
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.sendErr("", "bad_envelope", "malformed envelope")
			continue
		}
		return env, true
	}
}

func (c *client) readLoop(ctx context.Context) {
	for {
		env, ok := c.readEnvelope(ctx)
		if !ok {
			return
		}
		c.processEnvelope(ctx, env)
	}
}

// processEnvelope routes one command envelope. Only cmd is accepted here;
// subscribe is consumed by run before this is reached.
func (c *client) processEnvelope(ctx context.Context, env Envelope) {
	if env.Type != TCmd {
		c.sendErr(env.CID, "bad_type", "expected a cmd envelope")
		return
	}
	var cmd model.Command
	if err := json.Unmarshal(env.Data, &cmd); err != nil {
		c.sendErr(env.CID, "bad_command", "malformed command")
		return
	}
	if cmd.CID == "" {
		cmd.CID = env.CID // clients may correlate via either the envelope or the payload
	}
	c.handleCommand(ctx, cmd, env.Data)
}

func (c *client) handleCommand(ctx context.Context, cmd model.Command, raw json.RawMessage) {
	spec, ok := model.LookupCommand(cmd.Op)
	if !ok {
		c.sendErr(cmd.CID, "unknown_op", "unsupported command")
		return
	}
	switch spec.Layer {
	case model.LayerAccount:
		c.handleAccount(ctx, cmd, raw)
	case model.LayerBroker:
		c.handleInterest(cmd)
	case model.LayerManager:
		c.handleManager(cmd)
	case model.LayerSession:
		c.sendResult(c.bridge.manager.Dispatch(cmd))
	default:
		c.sendErr(cmd.CID, "unknown_layer", "command has no handler")
	}
}

func (c *client) handleAccount(ctx context.Context, cmd model.Command, raw json.RawMessage) {
	switch cmd.Op {
	case model.OpSetCredentials:
		// Credentials are parsed from the raw envelope, not model.Command, so a
		// password can never reach sessions, results, or logs.
		var creds struct {
			Account  string `json:"account"`
			Password string `json:"password"`
			Remember bool   `json:"remember"`
		}
		if err := json.Unmarshal(raw, &creds); err != nil || creds.Account == "" || creds.Password == "" {
			c.sendErr(cmd.CID, "missing_credentials", "account and password are required")
			return
		}
		st, err := c.bridge.account.SetCredentials(ctx, creds.Account, creds.Password, creds.Remember)
		if err != nil {
			if st.Status == model.AccountOK {
				// The pair is valid, but persisting it for the next restart failed.
				c.sendErr(cmd.CID, "persist_failed", "credentials accepted but could not be stored")
				return
			}
			c.sendErr(cmd.CID, "mint_failed", "could not reach F-List")
			return
		}
		c.sendAck(cmd.CID, nil)
	case model.OpPurgeCredentials:
		if err := c.bridge.account.PurgeStoredCredentials(ctx); err != nil {
			c.sendErr(cmd.CID, "purge_failed", "could not delete stored credentials")
			return
		}
		c.sendAck(cmd.CID, nil)
	case model.OpClearCredentials:
		c.bridge.account.ClearCredentials()
		c.sendAck(cmd.CID, nil)
	case model.OpListCharacters:
		c.enqueue(Envelope{Type: TAccountState, Data: rawJSON(c.bridge.account.State())})
		c.sendAck(cmd.CID, nil)
	default:
		c.sendErr(cmd.CID, "wrong_layer", "unhandled account command")
	}
}

func (c *client) handleInterest(cmd model.Command) {
	switch cmd.Level {
	case model.InterestNone, model.InterestSummary, model.InterestFull:
	default:
		c.sendErr(cmd.CID, "bad_level", "unknown interest level")
		return
	}
	c.sub.SetInterest(cmd.Session, cmd.Conv, cmd.Level, cmd.Since)
	c.sendAck(cmd.CID, nil)
}

func (c *client) handleManager(cmd model.Command) {
	switch cmd.Op {
	case model.OpLogin:
		account := c.bridge.account.Name()
		if account == "" {
			c.sendErr(cmd.CID, "no_credentials", "supply F-List credentials first")
			return
		}
		if cmd.Character == "" {
			c.sendErr(cmd.CID, "missing_character", "character is required")
			return
		}
		if err := c.bridge.manager.Login(account, cmd.Character); err != nil {
			c.sendErr(cmd.CID, "login_failed", err.Error())
			return
		}
		c.sendAck(cmd.CID, nil)
	case model.OpLogout:
		character := cmd.Session
		if character == "" {
			character = cmd.Character
		}
		c.bridge.manager.Logout(character)
		c.sendAck(cmd.CID, nil)
	case model.OpReconnect:
		c.sendResult(c.bridge.manager.Dispatch(cmd))
	default:
		c.sendErr(cmd.CID, "wrong_layer", "unhandled manager command")
	}
}

func (c *client) forwardBatches(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-c.sub.Events():
			if !ok {
				return
			}
			if !c.enqueue(Envelope{Type: TBatch, Data: rawJSON(broker.Batch{Events: batch.Events})}) {
				// The socket fell behind: hand the events back so the subscription
				// re-emits the latest value per key (or re-materializes a conv).
				c.sub.MarkDirtyBatch(batch.Events)
			}
		}
	}
}

func (c *client) forwardAccount(ctx context.Context, ch <-chan model.AccountState) {
	for {
		select {
		case <-ctx.Done():
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			c.enqueue(Envelope{Type: TAccountState, Data: rawJSON(st)})
		}
	}
}

func (c *client) writeLoop(ctx context.Context) {
	write := func(env Envelope) bool {
		data, err := json.Marshal(env)
		if err != nil {
			return true
		}
		writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
		err = c.ws.Write(writeCtx, websocket.MessageText, data)
		cancel()
		return err == nil
	}
	for {
		// Drain the urgent lane first so results are never queued behind batches.
		select {
		case env := <-c.urgent:
			if !write(env) {
				return
			}
		default:
		}
		select {
		case <-ctx.Done():
			return
		case env := <-c.urgent:
			if !write(env) {
				return
			}
		case env := <-c.send:
			if !write(env) {
				return
			}
		}
	}
}

// enqueue sends on the best-effort lane, reporting whether it fit. The caller
// decides how to recover a failure (forwardBatches marks the affected sessions
// for resync); a closed client reports success because it is going away.
func (c *client) enqueue(env Envelope) bool {
	select {
	case c.send <- env:
		return true
	case <-c.done:
		return true
	default:
		return false
	}
}

// enqueueUrgent sends an ack/error on the priority lane. A command result is
// never dropped silently: if the lane is full, the connection is cancelled so
// the client reconnects and resyncs, rather than waiting forever for a result
// that will never arrive. (Blocking instead would deadlock once writeLoop exits
// on its write timeout.)
func (c *client) enqueueUrgent(env Envelope) {
	select {
	case c.urgent <- env:
	case <-c.done:
	default:
		c.bridge.logger.Warn("ws: urgent queue full, closing client",
			"type", env.Type, "cid", env.CID)
		if c.cancel != nil {
			c.cancel()
		}
	}
}

func (c *client) sendResult(res model.Result) {
	env := Envelope{Type: TAck, CID: res.CID, Data: rawJSON(res)}
	if !res.Accepted {
		env.Type = TErr
	}
	c.enqueueUrgent(env)
}

func (c *client) sendAck(cid string, data any) {
	if data == nil {
		data = model.Result{CID: cid, Accepted: true}
	}
	c.enqueueUrgent(Envelope{Type: TAck, CID: cid, Data: rawJSON(data)})
}

func (c *client) sendErr(cid, code, msg string) {
	c.sendResult(model.Result{CID: cid, Accepted: false, ErrorCode: code, ErrorMsg: msg})
}
