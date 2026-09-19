package broker

import (
	"strings"
	"sync"

	"plexo/internal/model"
)

// deliveryPolicy is the seam between the transport-neutral fan-out core and
// F-Chat delivery semantics. It owns a subscriber's per-conversation interest
// and decides which canonical events reach it. Conversation membership and
// account-wide friends live in the broker, so this mirrors nothing.
type deliveryPolicy struct {
	broker *Broker
	opts   SubOpts

	mu       sync.Mutex
	interest map[interestKey]model.Interest
}

func newDeliveryPolicy(b *Broker, opts SubOpts) *deliveryPolicy {
	return &deliveryPolicy{broker: b, opts: opts, interest: map[interestKey]model.Interest{}}
}

// setInterest records a conversation's level. Last write wins.
func (p *deliveryPolicy) setInterest(session string, conv model.ConvRef, level model.Interest) {
	p.mu.Lock()
	p.interest[interestKey{session: session, conv: conv}] = level
	p.mu.Unlock()
}

// interestFor resolves a conversation's level, falling back to the
// subscription default.
func (p *deliveryPolicy) interestFor(session string, conv model.ConvRef) model.Interest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if lvl, ok := p.interest[interestKey{session: session, conv: conv}]; ok {
		return lvl
	}
	return p.opts.DefaultInterest
}

// deliver applies interest gating. Stream entries go only to full-interest
// conversations. State records are gated by their key namespace: account/*,
// session/*, invites/*, and search/* are always delivered; conv/* by
// conversation interest (summary or full); summary/* only at summary interest
// (a full subscriber derives activity from the stream entry); typing/* only at
// full; character/* only while the character is watched. Everything else
// (views, errors) is always delivered.
func (p *deliveryPolicy) deliver(ev model.Event) bool {
	switch ev.Kind {
	case model.EvMessage:
		msg, ok := ev.Payload.(model.MessagePayload)
		if !ok {
			return true
		}
		return p.interestFor(ev.Session, msg.Conv) == model.InterestFull
	case model.EvState:
		span, ok := ev.Payload.(model.StatePayload)
		if !ok {
			return true
		}
		return p.deliverState(ev.Session, span)
	default:
		return true
	}
}

// deliverState gates one state record by its key namespace. A removal whose
// value is absent is delivered to everyone: it is idempotent and a client that
// never had the key ignores it.
func (p *deliveryPolicy) deliverState(session string, span model.StatePayload) bool {
	switch model.KeyNamespace(span.Key) {
	case model.StateAccount, model.StateSession, model.StateSearch, model.StateInvites:
		return true
	case model.StateConv:
		if span.Removed {
			return true
		}
		v, ok := span.Value.(model.ConvStatePayload)
		if !ok {
			return true
		}
		lvl := p.interestFor(session, v.Conv)
		return lvl == model.InterestSummary || lvl == model.InterestFull
	case model.StateSummary:
		if span.Removed {
			return true
		}
		v, ok := span.Value.(model.SummaryPayload)
		if !ok {
			return true
		}
		return p.interestFor(session, v.Conv) == model.InterestSummary
	case model.StateTyping:
		if span.Removed {
			return true
		}
		v, ok := span.Value.(model.TypingPayload)
		if !ok {
			return true
		}
		return p.interestFor(session, v.Conv) == model.InterestFull
	case model.StateCharacter:
		v, ok := span.Value.(model.PresencePayload)
		if !ok {
			return true
		}
		return v.Character == session || p.watched(session, v.Character)
	default:
		return true
	}
}

// fullConvs returns every conversation this subscriber holds at full interest,
// for a broad resync that re-materializes each one.
func (p *deliveryPolicy) fullConvs() []interestKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]interestKey, 0, len(p.interest))
	for key, lvl := range p.interest {
		if lvl == model.InterestFull {
			out = append(out, key)
		}
	}
	return out
}

// watched reports whether character is rendered by this subscriber: an account
// friend/bookmark, or a member of any full-interest conversation. Membership is
// read from the broker's single shared map.
func (p *deliveryPolicy) watched(session, character string) bool {
	char := strings.ToLower(character)
	if p.broker == nil {
		return false
	}
	if p.broker.isFriend(char) {
		return true
	}
	return p.broker.watchConvs(session, char, func(conv model.ConvRef) bool {
		return p.interestFor(session, conv) == model.InterestFull
	})
}
