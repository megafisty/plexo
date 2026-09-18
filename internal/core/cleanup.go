package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"plexo/internal/model"
	"plexo/internal/store"
)

// ErrCleanupInvalid marks client input rejected by cleanup validation. The web
// layer maps it to 400.
var ErrCleanupInvalid = errors.New("core: invalid cleanup request")

// Cleanup bounds. The day cap is ten years; the entry cap keeps a client from
// sending an absurd threshold.
const (
	maxCleanupDays    = 36500
	maxCleanupEntries = 1_000_000
)

// CleanupQuery describes one chatlog cleanup operation.
//
//   - Op "age": delete entries older than Days across every conversation (or
//     just Session when set).
//   - Op "conversation": delete all of one conversation, or only its entries
//     older than Days when Days > 0.
//   - Op "dms": delete whole DM conversations inactive for Days that hold
//     fewer than MaxEntries entries.
//
// Apply false previews the impact; true performs the deletion and then reclaims
// disk space when enough has accumulated.
type CleanupQuery struct {
	Op         string
	Session    string
	Conv       *model.ConvRef
	Days       int
	MaxEntries int
	Apply      bool
}

// Cleanup validates q, previews or applies it, and (on apply) vacuums when the
// deletion freed enough pages. A failed vacuum is logged, not returned: the
// deletion already succeeded and its result is still reported.
func (m *Manager) Cleanup(ctx context.Context, q CleanupQuery) (model.CleanupResult, error) {
	pq, err := cleanupPruneQuery(q)
	if err != nil {
		return model.CleanupResult{}, err
	}
	var res store.PruneResult
	if q.Apply {
		res, err = m.cfg.Store.PruneHistory(ctx, pq)
	} else {
		res, err = m.cfg.Store.PrunePreview(ctx, pq)
	}
	if err != nil {
		return model.CleanupResult{}, err
	}
	out := model.CleanupResult{
		Conversations: res.Conversations,
		Entries:       res.Entries,
		Warpmarks:     res.Warpmarks,
		BodyBytes:     res.BodyBytes,
	}
	if q.Apply && res.Entries > 0 {
		vr, verr := m.cfg.Store.Vacuum(ctx)
		if verr != nil {
			if m.cfg.Logger != nil {
				m.cfg.Logger.Warn("chatlog vacuum failed", "err", verr)
			}
		} else {
			out.Vacuumed = vr.Vacuumed
			out.BytesReclaimed = vr.BytesReclaimed
		}
	}
	return out, nil
}

// cleanupPruneQuery validates a cleanup request and lowers it to a store prune.
func cleanupPruneQuery(q CleanupQuery) (store.PruneQuery, error) {
	switch q.Op {
	case "age":
		if err := validCleanupDays(q.Days); err != nil {
			return store.PruneQuery{}, err
		}
		return store.PruneQuery{Session: q.Session, OlderThanMs: cleanupCutoffMs(q.Days)}, nil
	case "conversation":
		if q.Session == "" || q.Conv == nil || q.Conv.ID == "" || !validCleanupConvKind(q.Conv.Kind) {
			return store.PruneQuery{}, fmt.Errorf("%w: session and a valid conversation are required", ErrCleanupInvalid)
		}
		pq := store.PruneQuery{Session: q.Session, Conv: q.Conv}
		if q.Days < 0 {
			return store.PruneQuery{}, fmt.Errorf("%w: days must not be negative", ErrCleanupInvalid)
		}
		if q.Days > 0 {
			if err := validCleanupDays(q.Days); err != nil {
				return store.PruneQuery{}, err
			}
			pq.OlderThanMs = cleanupCutoffMs(q.Days)
		}
		return pq, nil
	case "dms":
		if err := validCleanupDays(q.Days); err != nil {
			return store.PruneQuery{}, err
		}
		if q.MaxEntries < 1 || q.MaxEntries > maxCleanupEntries {
			return store.PruneQuery{}, fmt.Errorf("%w: maxEntries must be between 1 and %d", ErrCleanupInvalid, maxCleanupEntries)
		}
		return store.PruneQuery{
			Session:     q.Session,
			OlderThanMs: cleanupCutoffMs(q.Days),
			DMsOnly:     true,
			MaxEntries:  int64(q.MaxEntries),
		}, nil
	default:
		return store.PruneQuery{}, fmt.Errorf("%w: unknown operation", ErrCleanupInvalid)
	}
}

func validCleanupDays(days int) error {
	if days < 1 || days > maxCleanupDays {
		return fmt.Errorf("%w: days must be between 1 and %d", ErrCleanupInvalid, maxCleanupDays)
	}
	return nil
}

// validCleanupConvKind limits cleanup to the kinds the log browser can address.
// Broadcasts are not selectable and warp is a read-only alias.
func validCleanupConvKind(kind model.ConvKind) bool {
	switch kind {
	case model.ConvOfficial, model.ConvRoom, model.ConvDM:
		return true
	default:
		return false
	}
}

// cleanupCutoffMs is the exclusive created_at bound for a whole-day age.
func cleanupCutoffMs(days int) int64 {
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
}
