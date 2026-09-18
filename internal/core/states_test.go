package core_test

import "plexo/internal/model"

// stateOf returns the state record for an exact key.
func stateOf(ev model.Event, key string) (model.StatePayload, bool) {
	if ev.Kind != model.EvState {
		return model.StatePayload{}, false
	}
	sp, ok := ev.Payload.(model.StatePayload)
	if !ok || sp.Key != key {
		return model.StatePayload{}, false
	}
	return sp, true
}

// stateValue returns the typed value of the state record for an exact key.
func stateValue[T any](ev model.Event, key string) (T, bool) {
	var zero T
	sp, ok := stateOf(ev, key)
	if !ok {
		return zero, false
	}
	v, ok := sp.Value.(T)
	return v, ok
}

// nsValue returns the typed value of the first state record whose key is in the
// given namespace.
func nsValue[T any](ev model.Event, ns string) (T, bool) {
	var zero T
	if ev.Kind != model.EvState {
		return zero, false
	}
	sp, ok := ev.Payload.(model.StatePayload)
	if !ok || model.KeyNamespace(sp.Key) != ns {
		return zero, false
	}
	v, ok := sp.Value.(T)
	return v, ok
}

func officialConv(id string) model.ConvRef {
	return model.ConvRef{Kind: model.ConvOfficial, ID: id}
}
