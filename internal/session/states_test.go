package session

import "plexo/internal/model"

// stateFor reports whether ev is a state record for key, returning its payload.
// Tests use it to match on state keys instead of on the retired per-kind event
// kinds.
func stateFor(ev model.Event, key string) (model.StatePayload, bool) {
	if ev.Kind != model.EvState {
		return model.StatePayload{}, false
	}
	sp, ok := ev.Payload.(model.StatePayload)
	if !ok || sp.Key != key {
		return model.StatePayload{}, false
	}
	return sp, true
}
