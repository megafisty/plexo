package model

// Op names a UI-invokable primitive. Every user action the browser can take is
// exactly one of these; the core rejects any other op. Layer and Scope describe
// who handles it and what it acts on — see CommandCatalog.
type Op string

const (
	// Account and credentials (handled by the account service).
	OpSetCredentials   Op = "set_credentials"
	OpClearCredentials Op = "clear_credentials"
	OpPurgeCredentials Op = "purge_credentials"
	OpListCharacters   Op = "list_characters"

	// Session lifecycle (handled by the manager).
	OpLogin     Op = "login"
	OpLogout    Op = "logout"
	OpReconnect Op = "reconnect"

	// Conversation interaction (routed to the owning session actor).
	OpSendMessage Op = "send_message"
	OpSendLRP     Op = "send_lrp"
	OpSendTyping  Op = "send_typing"
	OpJoin        Op = "join"
	OpLeave       Op = "leave"
	OpSetStatus   Op = "set_status"
	OpSetIgnore   Op = "set_ignore"
	// OpSetBookmark adds or removes an F-List bookmark for a character. It is
	// account-scoped REST, not a chat frame, so it is handled by the account
	// layer.
	OpSetBookmark Op = "set_bookmark"
	// OpSetTracked marks a DM tracked (visible in the client's conversation
	// list) or untracked. The core owns this per-session set in memory; it is
	// intentionally not persisted and resets on restart.
	OpSetTracked Op = "set_tracked"

	// OpRoomAdmin creates a room or performs one administrative action on an
	// existing room. RoomAdminRequest carries the action and its parameters, so
	// the whole room-management write surface is one op.
	OpRoomAdmin Op = "room_admin"

	// OpDismissInvite drops one pending room invitation from the session's
	// invite list. Accepting an invitation (join) clears it automatically; this
	// op is how the client declines one so it does not reappear on resync.
	OpDismissInvite Op = "dismiss_invite"

	// Reads and delivery. History, ads, and presence search are request/response
	// shaped and served over HTTP (see internal/web); only live delivery
	// interest travels on the socket.
	OpSetInterest Op = "set_interest"
)

// CommandLayer identifies the component that handles a command.
type CommandLayer string

const (
	LayerAccount CommandLayer = "account" // credentials + account state
	LayerManager CommandLayer = "manager" // session registry and read paths
	LayerSession CommandLayer = "session" // routed to a session actor
	LayerBroker  CommandLayer = "broker"  // handled by the subscription
)

// CommandScope describes what a command acts on.
type CommandScope string

const (
	ScopeGlobal       CommandScope = "global"
	ScopeSession      CommandScope = "session"
	ScopeConversation CommandScope = "conversation"
)

// CommandSpec documents one primitive: who handles it, what it acts on, and
// which non-empty fields it requires.
type CommandSpec struct {
	Op      Op           `json:"op"`
	Layer   CommandLayer `json:"layer"`
	Scope   CommandScope `json:"scope"`
	Fields  []string     `json:"fields,omitempty"`
	Summary string       `json:"summary"`
}

var commandCatalog = []CommandSpec{
	{OpSetCredentials, LayerAccount, ScopeGlobal, []string{"account", "password"}, "Validate F-Chat credentials and optionally persist them; emits account_state."},
	{OpClearCredentials, LayerAccount, ScopeGlobal, nil, "Forget F-Chat credentials and the cached ticket in memory; stored credentials are left for the next restart."},
	{OpPurgeCredentials, LayerAccount, ScopeGlobal, nil, "Delete stored F-Chat credentials; running sessions keep their in-memory credentials until restart."},
	{OpListCharacters, LayerAccount, ScopeGlobal, nil, "Re-emit account_state with the character list."},
	{OpLogin, LayerManager, ScopeSession, []string{"character"}, "Start a session for a character using stored credentials."},
	{OpLogout, LayerManager, ScopeSession, nil, "Stop and remove a session."},
	{OpReconnect, LayerManager, ScopeSession, nil, "Re-run the full connect flow for a session."},
	{OpSendMessage, LayerSession, ScopeConversation, []string{"session", "conv", "body"}, "Send a channel or private message."},
	{OpSendLRP, LayerSession, ScopeConversation, []string{"session", "conv", "body"}, "Emit an LRP advertisement to a channel or room."},
	{OpSendTyping, LayerSession, ScopeConversation, []string{"session", "conv", "status"}, "Signal typing, paused, or clear for a private conversation."},
	{OpJoin, LayerSession, ScopeConversation, []string{"session", "conv"}, "Join a channel or room."},
	{OpLeave, LayerSession, ScopeConversation, []string{"session", "conv"}, "Leave a channel or room."},
	{OpSetStatus, LayerSession, ScopeSession, []string{"session", "status"}, "Change the character's status and status message."},
	{OpSetIgnore, LayerSession, ScopeSession, []string{"session", "action"}, "Add, delete, or list the account ignore list; character is required for add/delete."},
	{OpSetBookmark, LayerAccount, ScopeGlobal, []string{"character", "action"}, "Add or remove an F-List bookmark for a character."},
	{OpSetTracked, LayerSession, ScopeConversation, []string{"session", "conv", "tracked"}, "Track or untrack a DM so it appears in the client's conversation list."},
	{OpRoomAdmin, LayerSession, ScopeConversation, []string{"session", "room.action"}, "Create a room or administer one: describe, add/remove mod, kick, ban, unban, destroy, mode, visibility, set_owner, invite, timeout."},
	{OpDismissInvite, LayerSession, ScopeConversation, []string{"session", "conv"}, "Dismiss a pending room invitation so it is not offered again."},
	{OpSetInterest, LayerBroker, ScopeConversation, []string{"session", "conv", "level"}, "Set live delivery interest for a conversation."},
}

// Commands returns the full catalog of UI-invokable primitives.
func Commands() []CommandSpec {
	out := make([]CommandSpec, len(commandCatalog))
	copy(out, commandCatalog)
	return out
}

// LookupCommand returns the catalog entry for an op.
func LookupCommand(op Op) (CommandSpec, bool) {
	for _, spec := range commandCatalog {
		if spec.Op == op {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

// Result is the synchronous outcome of a command, correlated by CID.
type Result struct {
	CID       string `json:"cid"`
	Accepted  bool   `json:"accepted"`
	ErrorCode string `json:"errorCode,omitempty"`
	ErrorMsg  string `json:"errorMsg,omitempty"`
}
