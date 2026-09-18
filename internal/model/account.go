package model

// Account status values reported in account_state.
const (
	AccountMissing     = "missing"     // no credentials supplied
	AccountChecking    = "checking"    // a validation mint is in flight
	AccountOK          = "ok"          // credentials valid, characters known
	AccountInvalid     = "invalid"     // F-List rejected the credentials
	AccountUnreachable = "unreachable" // transient failure talking to F-List
)

// AccountState is the browser-facing account view. It never carries the
// password or the ticket.
type AccountState struct {
	Status     string   `json:"status"`
	Characters []string `json:"characters,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	// Persisted reports whether an F-Chat credential document is stored on the
	// core and will be restored after a restart. It never reveals the values.
	Persisted bool `json:"persisted"`
}
