package fchat

import (
	"encoding/json"
	"fmt"
)

// New builds a frame with a JSON payload. A nil payload produces a
// payload-less frame.
func New(code string, payload any) (Frame, error) {
	if payload == nil {
		return Frame{Code: code}, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Frame{}, fmt.Errorf("fchat: marshal %s: %w", code, err)
	}
	return Frame{Code: code, Data: data}, nil
}

// Decode unmarshals a frame's payload into T. A payload-less frame decodes
// to the zero value.
func Decode[T any](c Frame) (T, error) {
	var v T
	if len(c.Data) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(c.Data, &v); err != nil {
		return v, fmt.Errorf("fchat: decode %s: %w", c.Code, err)
	}
	return v, nil
}

// --- Client -> server command payloads ---

type IDNPayload struct {
	Method    string `json:"method"`
	Account   string `json:"account"`
	Ticket    string `json:"ticket"`
	Character string `json:"character"`
	CName     string `json:"cname"`
	CVersion  string `json:"cversion"`
}

type ChannelMsg struct {
	Channel string `json:"channel"`
	Message string `json:"message"`
}

type PrivateMsg struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type ChannelRef struct {
	Channel string `json:"channel"`
}

type StatusUpdate struct {
	Status    string `json:"status"`
	StatusMsg string `json:"statusmsg"`
}

type TypingNotification struct {
	Character string `json:"character"`
	Status    string `json:"status"`
}

// IgnoreUpdate is the client -> server IGN delta: action "add" or "delete".
type IgnoreUpdate struct {
	Character string `json:"character"`
	Action    string `json:"action"`
}

// IgnoreList is the client -> server IGN list request.
type IgnoreList struct {
	Action string `json:"action"`
}

// FKSRequest is the client -> server character search. Kinks are numeric kink
// ids; the enum filters are the value strings from the mapping. Kinks is
// required by the protocol, so it is never omitted.
type FKSRequest struct {
	Kinks        []int    `json:"kinks"`
	Genders      []string `json:"genders,omitempty"`
	Orientations []string `json:"orientations,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	FurryPrefs   []string `json:"furryprefs,omitempty"`
	Roles        []string `json:"roles,omitempty"`
}

// MarshalJSON keeps the required kinks field an array. A nil slice would
// otherwise serialize as null, which the server rejects.
func (r FKSRequest) MarshalJSON() ([]byte, error) {
	if r.Kinks == nil {
		r.Kinks = []int{}
	}
	type alias FKSRequest
	return json.Marshal(alias(r))
}

// --- Server -> client command payloads ---

type IDNEvent struct {
	Character string `json:"character"`
}

type HLOEvent struct {
	Message string `json:"message"`
}

type NLNEvent struct {
	Identity string `json:"identity"`
	Gender   string `json:"gender"`
	Status   string `json:"status"`
}

type FLNEvent struct {
	Character string `json:"character"`
}

type STAEvent struct {
	Character string `json:"character"`
	Status    string `json:"status"`
	StatusMsg string `json:"statusmsg"`
}

type MSGEvent struct {
	Character string `json:"character"`
	Channel   string `json:"channel"`
	Message   string `json:"message"`
}

type PRIEvent struct {
	Character string `json:"character"`
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type LRPEvent struct {
	Character string `json:"character"`
	Channel   string `json:"channel"`
	Message   string `json:"message"`
}

type RLLEvent struct {
	Character string   `json:"character"`
	Channel   string   `json:"channel"`
	Recipient string   `json:"recipient"`
	Message   string   `json:"message"`
	Type      string   `json:"type"`
	Rolls     []string `json:"rolls"`
	Results   []int    `json:"results"`
	EndResult int      `json:"endresult"`
	Target    string   `json:"target"`
}

type TPNEvent struct {
	Character string `json:"character"`
	Status    string `json:"status"`
}

type CONEvent struct {
	Count int `json:"count"`
}

type LISEvent struct {
	Characters [][]string `json:"characters"`
}

type ADLEvent struct {
	Ops []string `json:"ops"`
}

// AOPEvent and DOPEvent are the incremental updates to the global moderator
// list that ADL seeds. Both carry a single character.
type AOPEvent struct {
	Character string `json:"character"`
}

type DOPEvent struct {
	Character string `json:"character"`
}

type FRLEvent struct {
	Characters []string `json:"characters"`
}

// RTBEvent is a realtime-bridge message relaying a change made on the F-List
// website (bookmarks, friends). Type is trackadd/trackrem/friendadd/
// friendremove; Name is the affected character.
type RTBEvent struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// IgnoreEvent is the server -> client IGN frame. Action "init" carries the
// full Characters list; "add"/"delete" carry a single Character.
type IgnoreEvent struct {
	Characters []string `json:"characters"`
	Character  string   `json:"character"`
	Action     string   `json:"action"`
}

// FKSEvent is the server -> client search reply. Characters are character
// names; Kinks echoes the ids that were searched on.
type FKSEvent struct {
	Characters []string `json:"characters"`
	Kinks      []int    `json:"kinks"`
}

// OfficialChannel is one entry of a CHA reply.
type OfficialChannel struct {
	Name       string `json:"name"`
	Characters int    `json:"characters"`
}

// PublicRoom is one entry of an ORS reply.
type PublicRoom struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Characters int    `json:"characters"`
}

type CHAEvent struct {
	Channels []OfficialChannel `json:"channels"`
}

type ORSEvent struct {
	Channels []PublicRoom `json:"channels"`
}

type VAREvent struct {
	Variable string          `json:"variable"`
	Value    json.RawMessage `json:"value"`
}

// NameOrIdentity accepts either a bare character name (string) or an object
// with an "identity" field, both of which appear in the protocol.
type NameOrIdentity struct {
	Name string
}

func (n *NameOrIdentity) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		n.Name = s
		return nil
	}
	var o struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	n.Name = o.Identity
	return nil
}

func (n NameOrIdentity) MarshalJSON() ([]byte, error) { return json.Marshal(n.Name) }

type JCHEvent struct {
	Channel   string         `json:"channel"`
	Title     string         `json:"title"`
	Character NameOrIdentity `json:"character"`
	Mode      string         `json:"mode"`
}

type LCHEvent struct {
	Channel   string         `json:"channel"`
	Character NameOrIdentity `json:"character"`
}

type ICHEvent struct {
	Channel string           `json:"channel"`
	Users   []NameOrIdentity `json:"users"`
	Mode    string           `json:"mode"`
}

type CDSEvent struct {
	Channel     string `json:"channel"`
	Description string `json:"description"`
}

// RMOEvent is a channel message-mode change. The server sends it instead of a
// fresh ICH when a moderator switches a channel between "chat", "ads", and
// "both".
type RMOEvent struct {
	Channel string `json:"channel"`
	Mode    string `json:"mode"`
}

type COLEvent struct {
	Channel string   `json:"channel"`
	OpList  []string `json:"oplist"`
}

// COAEvent and COREvent are the channel-op deltas. The channel is an id, not a
// title, and the character is case sensitive on the wire.
type COAEvent struct {
	Channel   string `json:"channel"`
	Character string `json:"character"`
}

type COREvent struct {
	Channel   string `json:"channel"`
	Character string `json:"character"`
}

type BROEvent struct {
	Character string `json:"character"`
	Message   string `json:"message"`
}

// EREvent models the error command. The field is documented as "code" in the
// current API but appears as "number" in older material; accept both.
type EREvent struct {
	Code    int    `json:"code"`
	Number  int    `json:"number"`
	Message string `json:"message"`
}

// EffectiveCode returns whichever of code/number was provided.
func (e EREvent) EffectiveCode() int {
	if e.Code != 0 {
		return e.Code
	}
	return e.Number
}
