package session

import (
	"strings"
	"sync"

	"plexo/internal/model"
)

// defaultAdCapacity is the number of LRP ads a session retains before the
// oldest are evicted.
const defaultAdCapacity = 200

// adBuffer is a per-session FIFO of LRP advertisements indexed by posting
// character. Ads are a sideline: they belong to no conversation and are never
// persisted. An ad from a character already present is ignored, so a
// character's ad leaves only by aging out; this assumes a character re-posts
// the same ad. Safe for concurrent use.
type adBuffer struct {
	mu       sync.Mutex
	capacity int
	order    []string // fixed-size ring of character keys, oldest at head
	head     int      // index of the oldest key
	size     int      // number of buffered keys
	byChar   map[string]model.Ad
}

func newAdBuffer(capacity int) *adBuffer {
	if capacity <= 0 {
		capacity = defaultAdCapacity
	}
	return &adBuffer{capacity: capacity, order: make([]string, capacity), byChar: map[string]model.Ad{}}
}

func adKey(character string) string { return strings.ToLower(character) }

// add stores ad unless the character already has one buffered. It reports
// whether the ad was accepted. If accepted, the oldest ad is evicted when the
// buffer is over capacity.
func (b *adBuffer) add(ad model.Ad) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := adKey(ad.Character)
	if _, ok := b.byChar[key]; ok {
		return false
	}
	b.byChar[key] = ad
	if b.size < b.capacity {
		b.order[(b.head+b.size)%b.capacity] = key
		b.size++
	} else {
		// Evict the oldest key and reuse its slot.
		delete(b.byChar, b.order[b.head])
		b.order[b.head] = key
		b.head = (b.head + 1) % b.capacity
	}
	return true
}

// list returns the buffered ads, oldest first.
func (b *adBuffer) list() []model.Ad {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]model.Ad, 0, b.size)
	for i := 0; i < b.size; i++ {
		out = append(out, b.byChar[b.order[(b.head+i)%b.capacity]])
	}
	return out
}

// len returns the number of buffered ads.
func (b *adBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}
