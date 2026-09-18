// F-Chat API tickets. A ticket is needed only at IDN and for unauthenticated
// REST calls; live sessions do not need one. Minting is lazy and serialized
// because a new ticket invalidates the previous one.
package fchat

import (
	"context"
	"time"
)

// Ticket is a shared, per-account API ticket together with the account's
// character list, which the ticket endpoint returns alongside it.
type Ticket struct {
	Value      string
	MintedAt   time.Time
	Characters []string
}

// TicketManager yields a valid ticket for an account, minting or reusing a
// cached one as needed.
type TicketManager interface {
	Ticket(ctx context.Context, account string) (Ticket, error)
}

// TicketInvalidator is implemented by ticket managers that cache tickets.
// Sessions call it after an IDENT_FAILED so a manual reconnect mints a fresh
// ticket instead of replaying the rejected one.
type TicketInvalidator interface {
	Invalidate(account string)
}

// TicketFunc adapts a function to TicketManager.
type TicketFunc func(ctx context.Context, account string) (Ticket, error)

// Ticket implements TicketManager.
func (f TicketFunc) Ticket(ctx context.Context, account string) (Ticket, error) {
	return f(ctx, account)
}
