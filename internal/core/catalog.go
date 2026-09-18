// Core-wide channel catalog: the official channel list and the public room
// list. Fetched through whichever session reports a stale catalog first (the
// post-NLN tick and every server PIN). Never persisted; the catalog is
// rebuilt after a core restart.
package core

import (
	"sync"
	"time"

	"plexo/internal/model"
	"plexo/internal/session"
)

// RoomsTTL is how long the public room list is considered fresh. Official
// channels are fetched once per core lifetime; refreshes are cheap to skip
// because that list almost never changes.
var RoomsTTL = 30 * time.Minute

type channelCatalog struct {
	mu       sync.Mutex
	official []model.OfficialChannel
	rooms    []model.PublicRoom
	// officialAt/roomsAt are zero until a reply has landed.
	officialAt time.Time
	roomsAt    time.Time
	// chaRequestedBy/orsRequestedBy hold the character with a CHA/ORS round trip
	// in flight. They are tracked independently so a half-failure (one send
	// rejected) cannot clear the other request's marker and let a duplicate
	// request race the one still in flight.
	chaRequestedBy string
	orsRequestedBy string
}

// onStale implements session.Config.OnStale: it runs after the self NLN and
// on every PIN, and sends CHA/ORS through the reporting session if (and only
// if) the catalog is out of date and no request is in flight.
func (m *Manager) onStale(s *session.Session) {
	m.cat.mu.Lock()
	needCHA := m.cat.officialAt.IsZero()
	needORS := time.Since(m.cat.roomsAt) >= RoomsTTL
	if !needCHA && !needORS {
		m.cat.mu.Unlock()
		return
	}
	// Reserve each request independently. An empty marker is free; a marker
	// owned by a dead session is voided; a marker owned by a live other session
	// means the request is genuinely in flight elsewhere.
	reserve := func(marker *string) bool {
		if *marker == "" {
			*marker = s.Character()
			return true
		}
		if *marker == s.Character() {
			return false
		}
		if _, ok := m.Session(*marker); ok {
			return false
		}
		*marker = s.Character()
		return true
	}
	gotCHA := needCHA && reserve(&m.cat.chaRequestedBy)
	gotORS := needORS && reserve(&m.cat.orsRequestedBy)
	m.cat.mu.Unlock()

	failed := false
	if gotCHA {
		if err := s.RequestOfficialChannels(); err != nil {
			failed = true
		}
	}
	if gotORS {
		if err := s.RequestPublicRooms(); err != nil {
			failed = true
		}
	}
	if !failed {
		return
	}
	m.cat.mu.Lock()
	if gotCHA && m.cat.chaRequestedBy == s.Character() {
		m.cat.chaRequestedBy = ""
	}
	if gotORS && m.cat.orsRequestedBy == s.Character() {
		m.cat.orsRequestedBy = ""
	}
	m.cat.mu.Unlock()
}

// onCatalog implements session.Config.OnCatalog: CHA/ORS replies land here.
// The updated half is stored, the in-flight marker cleared, and the whole
// catalog published as one set-to event for live subscribers.
func (m *Manager) onCatalog(character string, official []model.OfficialChannel, rooms []model.PublicRoom) {
	m.cat.mu.Lock()
	now := time.Now()
	if official != nil {
		m.cat.official = official
		m.cat.officialAt = now
		m.cat.chaRequestedBy = ""
	}
	if rooms != nil {
		m.cat.rooms = rooms
		m.cat.roomsAt = now
		m.cat.orsRequestedBy = ""
	}
	payload := model.ChannelCatalogPayload{
		Official: append([]model.OfficialChannel(nil), m.cat.official...),
		Rooms:    append([]model.PublicRoom(nil), m.cat.rooms...),
	}
	m.cat.mu.Unlock()

	m.broker.Publish(model.Event{
		Session: character,
		Kind:    model.EvState,
		Time:    now,
		Payload: model.StatePayload{Key: model.AccountKey("catalog"), Value: payload},
	})
}

// catalogSnapshot returns a copy of the current catalog for Snapshot().
func (m *Manager) catalogSnapshot() model.ChannelCatalogPayload {
	m.cat.mu.Lock()
	defer m.cat.mu.Unlock()
	return model.ChannelCatalogPayload{
		Official: append([]model.OfficialChannel(nil), m.cat.official...),
		Rooms:    append([]model.PublicRoom(nil), m.cat.rooms...),
	}
}
