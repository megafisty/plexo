// Package fakeserver is a scriptable in-process F-Chat server used to exercise
// the whole chat pipeline without a network. It implements just enough of the
// protocol for login, hydration, and message echo.
package fakeserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"plexo/internal/fchat"
	"plexo/test/fchatpipe"
)

// Options configures the fake server's login behavior and hydration payload.
type Options struct {
	Account   string
	Character string
	Gender    string
	Status    string
	Roster    [][]string              // positional LIS rows: name, gender, status, statusMsg
	Friends   []string                // FRL union of the account's bookmarks and friends
	Ignores   []string                // account ignore list sent as an IGN init on login
	Channels  []fchat.OfficialChannel // CHA reply
	Rooms     []fchat.PublicRoom      // ORS reply
	Vars      map[string]any
	PinEvery  time.Duration // server-initiated PIN cadence; 0 disables

	// SearchResults is the character list returned for an FKS request.
	// SearchErr, when non-zero, answers FKS with that ERR code instead.
	SearchResults []string
	SearchErr     int
}

// Server speaks the server side of the F-Chat protocol over one connection.
type Server struct {
	conn fchat.Conn
	opts Options

	mu        sync.Mutex
	character string
	received  chan fchat.Frame
	ignores   map[string]struct{}
	rooms     map[string]*fakeRoom
	roomSeq   int
}

// fakeRoom is the fake server's minimal room model, enough to echo the frames
// the room-management verbs produce. It is per-connection: each dial creates a
// fresh Server.
type fakeRoom struct {
	id          string
	title       string
	description string
	mode        string
	owner       string
	ops         []string
	public      bool
}

// New creates a fake server bound to conn.
func New(conn fchat.Conn, opts Options) *Server {
	if opts.Gender == "" {
		opts.Gender = "Female"
	}
	if opts.Status == "" {
		opts.Status = "online"
	}
	return &Server{
		conn:     conn,
		opts:     opts,
		received: make(chan fchat.Frame, 256),
		ignores:  ignoreSet(opts.Ignores),
		rooms:    map[string]*fakeRoom{},
	}
}

// Received yields every command the client sent. It is a tap for assertions.
func (s *Server) Received() <-chan fchat.Frame { return s.received }

// Run serves the connection until it closes.
func (s *Server) Run(ctx context.Context) {
	if s.opts.PinEvery > 0 {
		go s.pinLoop(ctx)
	}
	for {
		cmd, err := s.conn.Read(ctx)
		if err != nil {
			return
		}
		select {
		case s.received <- cmd:
		case <-ctx.Done():
			return
		}
		s.handle(ctx, cmd)
	}
}

// pinLoop sends server-initiated PINs so the client's periodic-refresh
// triggers fire like they do against the real server.
func (s *Server) pinLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.PinEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.send(ctx, "PIN", nil)
		}
	}
}

// Send pushes a server command to the client (e.g. a message from another
// character).
func (s *Server) Send(code string, payload any) error {
	return s.send(context.Background(), code, payload)
}

// Close drops the connection, simulating a network failure or server-side
// termination.
func (s *Server) Close() error { return s.conn.Close() }

// SendRawText writes raw text as a single WebSocket message, bypassing frame
// encoding. It lets tests inject malformed or wrong-typed commands. Only the
// WebSocket transport supports it; the in-memory pipe reports an error.
func (s *Server) SendRawText(text string) error {
	if rc, ok := s.conn.(interface{ writeText([]byte) error }); ok {
		return rc.writeText([]byte(text))
	}
	return errors.New("fakeserver: raw text is unsupported over this transport")
}

// Character returns the character name established at login.
func (s *Server) Character() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.character
}

func (s *Server) send(ctx context.Context, code string, payload any) error {
	cmd, err := fchat.New(code, payload)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, cmd)
}

func (s *Server) handle(ctx context.Context, cmd fchat.Frame) {
	switch cmd.Code {
	case "IDN":
		p, _ := fchat.Decode[fchat.IDNPayload](cmd)
		s.mu.Lock()
		s.character = p.Character
		if s.character == "" {
			s.character = s.opts.Character
		}
		self := s.character
		s.mu.Unlock()
		s.hydrate(ctx, self)
	case "MSG", "PRI", "LRP":
		// The real server delivers a message to everyone except its sender
		// (`Channel::sendToChannel` skips the source and `event.PRI` targets
		// only the recipient), and this fake has a single connection, so there
		// is nobody to echo to. The core records the sender's own copy.
	case "JCH":
		p, _ := fchat.Decode[fchat.ChannelRef](cmd)
		self := s.Character()
		title := p.Channel
		for _, ch := range s.opts.Channels {
			if ch.Name == p.Channel {
				title = ch.Name
			}
		}
		for _, r := range s.opts.Rooms {
			if r.Name == p.Channel || r.Title == p.Channel {
				if r.Title != "" {
					title = r.Title
				} else {
					title = r.Name
				}
			}
		}
		_ = s.send(ctx, "JCH", fchat.JCHEvent{
			Channel:   p.Channel,
			Title:     title,
			Character: fchat.NameOrIdentity{Name: self},
			Mode:      "chat",
		})
		_ = s.send(ctx, "ICH", fchat.ICHEvent{
			Channel: p.Channel,
			Users:   []fchat.NameOrIdentity{{Name: self}},
			Mode:    "chat",
		})
		_ = s.send(ctx, "CDS", fchat.CDSEvent{Channel: p.Channel, Description: "Fake channel"})
	case "LCH":
		p, _ := fchat.Decode[fchat.ChannelRef](cmd)
		self := s.Character()
		_ = s.send(ctx, "LCH", fchat.LCHEvent{Channel: p.Channel, Character: fchat.NameOrIdentity{Name: self}})
	case "CCR":
		p, _ := fchat.Decode[fchat.ChannelRef](cmd)
		self := s.Character()
		r := s.createRoom(self, p.Channel)
		s.joinRoom(ctx, r, self)
	case "COL":
		p, _ := fchat.Decode[fchat.ChannelRef](cmd)
		if r, ok := s.getRoom(p.Channel); ok {
			s.sendCOL(ctx, r)
		}
	case "RST":
		p, _ := fchat.Decode[fchat.RoomPublic](cmd)
		if _, ok := s.mutateRoom(p.Channel, func(r *fakeRoom) { r.public = p.Status == "public" }); ok {
			// The real server answers only the requester with a SYS and broadcasts
			// nothing.
			_ = s.send(ctx, "SYS", map[string]string{"message": "Room visibility updated."})
		}
	case "RMO":
		p, _ := fchat.Decode[fchat.RoomMode](cmd)
		if r, ok := s.mutateRoom(p.Channel, func(r *fakeRoom) { r.mode = p.Mode }); ok {
			_ = s.send(ctx, "RMO", fchat.RMOEvent{Channel: r.id, Mode: r.mode})
		}
	case "CIU":
		p, _ := fchat.Decode[fchat.ChannelCharacter](cmd)
		if r, ok := s.getRoom(p.Channel); ok {
			// The fake is per-connection, so the target is not necessarily here.
			// Deliver the invitation only when the connected client is the target,
			// which lets a test drive the inbound path.
			if strings.EqualFold(p.Character, s.Character()) {
				_ = s.send(ctx, "CIU", fchat.CIUEvent{Sender: s.Character(), Title: r.title, Name: r.id})
			}
			_ = s.send(ctx, "SYS", map[string]string{"message": "Your invitation has been sent."})
		}
	case "CTU":
		p, _ := fchat.Decode[fchat.RoomTimeout](cmd)
		if r, ok := s.getRoom(p.Channel); ok {
			_ = s.send(ctx, "CTU", fchat.CTUEvent{Channel: r.id, Character: p.Character, Operator: s.Character(), Length: p.Length})
			_ = s.send(ctx, "LCH", fchat.LCHEvent{Channel: r.id, Character: fchat.NameOrIdentity{Name: p.Character}})
		}
	case "CDS":
		p, _ := fchat.Decode[fchat.ChannelDescription](cmd)
		if r, ok := s.mutateRoom(p.Channel, func(r *fakeRoom) { r.description = p.Description }); ok {
			_ = s.send(ctx, "CDS", fchat.CDSEvent{Channel: r.id, Description: r.description})
		}
	case "COA":
		p, _ := fchat.Decode[fchat.ChannelCharacter](cmd)
		if r, ok := s.mutateRoom(p.Channel, func(r *fakeRoom) { r.ops = append(r.ops, p.Character) }); ok {
			_ = s.send(ctx, "COA", fchat.COAEvent{Channel: r.id, Character: p.Character})
			s.sendCOL(ctx, r)
		}
	case "COR":
		p, _ := fchat.Decode[fchat.ChannelCharacter](cmd)
		if r, ok := s.mutateRoom(p.Channel, func(r *fakeRoom) {
			r.ops = removeString(r.ops, p.Character)
		}); ok {
			_ = s.send(ctx, "COR", fchat.COREvent{Channel: r.id, Character: p.Character})
			s.sendCOL(ctx, r)
		}
	case "CSO":
		p, _ := fchat.Decode[fchat.ChannelCharacter](cmd)
		if r, ok := s.mutateRoom(p.Channel, func(r *fakeRoom) {
			r.owner = p.Character
			r.ops = removeString(r.ops, p.Character)
		}); ok {
			_ = s.send(ctx, "CSO", fchat.CSOEvent{Channel: r.id, Character: p.Character})
			s.sendCOL(ctx, r)
		}
	case "CBU", "CKU":
		p, _ := fchat.Decode[fchat.ChannelCharacter](cmd)
		self := s.Character()
		if r, ok := s.getRoom(p.Channel); ok {
			if cmd.Code == "CBU" {
				_ = s.send(ctx, "CBU", fchat.CBUEvent{Channel: r.id, Character: p.Character, Operator: self})
			} else {
				_ = s.send(ctx, "CKU", fchat.CKUEvent{Channel: r.id, Character: p.Character, Operator: self})
			}
			_ = s.send(ctx, "LCH", fchat.LCHEvent{Channel: r.id, Character: fchat.NameOrIdentity{Name: p.Character}})
		}
	case "CUB":
		// CUB has no broadcast; the reply is a SYS only the caller sees. Nothing
		// to send for a single-connection fake.
	case "KIC":
		p, _ := fchat.Decode[fchat.ChannelRef](cmd)
		self := s.Character()
		if r, ok := s.getRoom(p.Channel); ok {
			_ = s.send(ctx, "BRO", fchat.BROEvent{Message: "destroyed"})
			_ = s.send(ctx, "LCH", fchat.LCHEvent{Channel: r.id, Character: fchat.NameOrIdentity{Name: self}})
		}
	case "STA":
		p, _ := fchat.Decode[fchat.StatusUpdate](cmd)
		self := s.Character()
		_ = s.send(ctx, "STA", fchat.STAEvent{Character: self, Status: p.Status, StatusMsg: p.StatusMsg})
	case "CHA":
		_ = s.send(ctx, "CHA", fchat.CHAEvent{Channels: s.opts.Channels})
	case "ORS":
		_ = s.send(ctx, "ORS", fchat.ORSEvent{Channels: append(append([]fchat.PublicRoom(nil), s.opts.Rooms...), s.publicRooms()...)})
	case "IGN":
		p, _ := fchat.Decode[fchat.IgnoreEvent](cmd)
		if p.Action == "list" {
			_ = s.send(ctx, "IGN", fchat.IgnoreEvent{Characters: s.ignoreList(), Action: "init"})
			return
		}
		s.mu.Lock()
		switch p.Action {
		case "add":
			if p.Character != "" {
				s.ignores[p.Character] = struct{}{}
			}
		case "delete":
			delete(s.ignores, p.Character)
		}
		s.mu.Unlock()
		_ = s.send(ctx, "IGN", fchat.IgnoreEvent{Character: p.Character, Action: p.Action})
	case "FKS":
		p, _ := fchat.Decode[fchat.FKSRequest](cmd)
		if s.opts.SearchErr != 0 {
			_ = s.send(ctx, "ERR", fchat.EREvent{Code: s.opts.SearchErr, Message: "fake search error"})
			return
		}
		_ = s.send(ctx, "FKS", fchat.FKSEvent{Characters: s.opts.SearchResults, Kinks: p.Kinks})
	}
}

func (s *Server) hydrate(ctx context.Context, self string) {
	_ = s.send(ctx, "HLO", fchat.HLOEvent{Message: "Fake F-Chat 1.0"})
	_ = s.send(ctx, "IDN", fchat.IDNEvent{Character: self})
	_ = s.send(ctx, "NLN", fchat.NLNEvent{Identity: self, Gender: s.opts.Gender, Status: s.opts.Status})

	rows := append([][]string{{self, s.opts.Gender, s.opts.Status, ""}}, s.opts.Roster...)
	_ = s.send(ctx, "CON", fchat.CONEvent{Count: len(rows)})
	_ = s.send(ctx, "LIS", fchat.LISEvent{Characters: rows})
	_ = s.send(ctx, "ADL", fchat.ADLEvent{Ops: []string{"GlobalAdmin"}})
	friends := s.opts.Friends
	if friends == nil {
		friends = []string{"BestFriend"}
	}
	_ = s.send(ctx, "FRL", fchat.FRLEvent{Characters: friends})
	_ = s.send(ctx, "IGN", fchat.IgnoreEvent{Characters: s.ignoreList(), Action: "init"})

	vars := s.opts.Vars
	if vars == nil {
		vars = map[string]any{"chat_max": 4096, "priv_max": 50000, "msg_flood": 0.0, "lfrp_flood": 0.0}
	}
	for k, v := range vars {
		data, _ := fchat.New("VAR", map[string]any{"variable": k, "value": v})
		_ = s.sendRaw(ctx, data)
	}
}

func (s *Server) sendRaw(ctx context.Context, cmd fchat.Frame) error {
	return s.conn.Write(ctx, cmd)
}

// createRoom registers a new room owned by self and returns a snapshot. A new
// room is closed (private): it does not appear in ORS until RST publishes it.
func (s *Server) createRoom(self, title string) fakeRoom {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roomSeq++
	r := &fakeRoom{
		id:          fmt.Sprintf("ADH-fake%04d", s.roomSeq),
		title:       title,
		description: "Fake room",
		mode:        "both",
		owner:       self,
		public:      false,
	}
	s.rooms[r.id] = r
	return *r
}

// publicRooms returns the currently published fake rooms in the ORS shape.
func (s *Server) publicRooms() []fchat.PublicRoom {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fchat.PublicRoom, 0, len(s.rooms))
	for _, r := range s.rooms {
		if r.public {
			out = append(out, fchat.PublicRoom{Name: r.id, Title: r.title, Characters: 1})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// getRoom returns a snapshot of a room.
func (s *Server) getRoom(id string) (fakeRoom, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[id]
	if !ok {
		return fakeRoom{}, false
	}
	return *r, true
}

// mutateRoom applies f under the lock and returns a snapshot of the result.
func (s *Server) mutateRoom(id string, f func(*fakeRoom)) (fakeRoom, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[id]
	if !ok {
		return fakeRoom{}, false
	}
	f(r)
	return *r, true
}

// joinRoom emits the frames a join produces: JCH, ICH, COL (owner first), CDS.
func (s *Server) joinRoom(ctx context.Context, r fakeRoom, self string) {
	_ = s.send(ctx, "JCH", fchat.JCHEvent{Channel: r.id, Title: r.title, Character: fchat.NameOrIdentity{Name: self}, Mode: r.mode})
	_ = s.send(ctx, "ICH", fchat.ICHEvent{Channel: r.id, Users: []fchat.NameOrIdentity{{Name: self}}, Mode: r.mode})
	s.sendCOL(ctx, r)
	_ = s.send(ctx, "CDS", fchat.CDSEvent{Channel: r.id, Description: r.description})
}

// sendCOL emits a COL with the owner in the first slot, as the real server does.
func (s *Server) sendCOL(ctx context.Context, r fakeRoom) {
	oplist := make([]string, 0, len(r.ops)+1)
	oplist = append(oplist, r.owner)
	oplist = append(oplist, r.ops...)
	_ = s.send(ctx, "COL", fchat.COLEvent{Channel: r.id, OpList: oplist})
}

// removeString returns names without the first case-insensitive match to want.
func removeString(names []string, want string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !strings.EqualFold(n, want) {
			out = append(out, n)
		}
	}
	return out
}

// ignoreList returns a snapshot of the current ignore set.
func (s *Server) ignoreList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.ignores)
}

func ignoreSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Factory produces connections to fresh fake servers. Each Dial call creates a
// new server, which lets tests exercise reconnect behavior.
type Factory struct {
	opts Options

	// ws is non-nil when the factory dials a real WebSocket server.
	ws *WSServer

	mu      sync.Mutex
	servers []*Server
}

// NewWSFactory creates a dial factory backed by a TLS WebSocket server. Each
// Dial goes through the production fchat transport, so tests exercise real
// handshakes and framing.
func NewWSFactory(opts Options) *Factory {
	f := &Factory{opts: opts}
	f.ws = newWSServer(opts, f.add)
	return f
}

// Close releases the WebSocket server, if any. It is safe on pipe factories.
func (f *Factory) Close() {
	if f.ws != nil {
		f.ws.close()
	}
}

func (f *Factory) add(srv *Server) {
	f.mu.Lock()
	f.servers = append(f.servers, srv)
	f.mu.Unlock()
}

// Dial opens a new connection to a fresh fake server.
func (f *Factory) Dial(ctx context.Context) (fchat.Conn, error) {
	if f.ws != nil {
		return f.ws.Dial(ctx)
	}
	client, server := fchatpipe.Pipe()
	srv := New(server, f.opts)
	f.add(srv)
	go srv.Run(ctx)
	return client, nil
}

// Last returns the most recently created server, or nil.
func (f *Factory) Last() *Server {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.servers) == 0 {
		return nil
	}
	return f.servers[len(f.servers)-1]
}

// First returns the first server created, or nil.
func (f *Factory) First() *Server {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.servers) == 0 {
		return nil
	}
	return f.servers[0]
}

// Count returns the number of servers created.
func (f *Factory) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.servers)
}
