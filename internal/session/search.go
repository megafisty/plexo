package session

import (
	"plexo/internal/fchat"
	"plexo/internal/model"
)

// handleSearch queues an FKS on the session's connection. It runs on the actor
// goroutine. The reply is cached on the session when it arrives, so submission
// only reports acceptance.
func (s *Session) handleSearch(q model.SearchQuery) model.Result {
	if s.st.phase != "ready" {
		return model.Result{ErrorCode: "not_ready", ErrorMsg: "session is not ready"}
	}
	if err := s.queue("FKS", toFKSRequest(q)); err != nil {
		return model.Result{ErrorCode: "send_failed", ErrorMsg: err.Error()}
	}
	return model.Result{Accepted: true}
}

// publishSearch caches a new enriched FKS result set and announces that it is
// available. The rows themselves are pulled over HTTP (SearchResults), so the
// event carries only the revision.
func (s *Session) publishSearch(members []model.MemberInfo) {
	s.st.search = members
	s.st.searchRev++
	s.emitState(model.SearchKey(s.cfg.Character), model.SearchNotice{Revision: s.st.searchRev})
}

// clearSearch drops the cached FKS result set. Results are connection-scoped
// (their presence was read from that connection's roster), so a reconnect must
// not serve them. It notifies subscribers only when something was cached.
func (s *Session) clearSearch() {
	if len(s.st.search) == 0 {
		return
	}
	s.st.search = []model.MemberInfo{}
	s.st.searchRev++
	s.emitState(model.SearchKey(s.cfg.Character), model.SearchNotice{Revision: s.st.searchRev})
}

// searchResultsLocked reads the cached result set. It runs on the actor
// goroutine.
func (s *Session) searchResultsLocked() model.SearchPayload {
	chars := s.st.search
	if chars == nil {
		chars = []model.MemberInfo{}
	}
	return model.SearchPayload{Characters: chars, Revision: s.st.searchRev}
}

// SearchResults returns the session's cached FKS result set and its revision.
// It is served on demand over HTTP so the enriched rows never ride the event
// socket. A stopped session reads as empty at revision zero.
func (s *Session) SearchResults() model.SearchPayload {
	r, _ := ask(s, func(reply chan model.SearchPayload) { reply <- s.searchResultsLocked() })
	return r
}

// toFKSRequest maps the web-facing query to the wire payload. Kinks are
// required by the protocol; FKSRequest.MarshalJSON guarantees the field is
// always an array even when the caller leaves it nil.
func toFKSRequest(q model.SearchQuery) fchat.FKSRequest {
	return fchat.FKSRequest{
		Kinks:        q.Kinks,
		Genders:      q.Genders,
		Orientations: q.Orientations,
		Languages:    q.Languages,
		FurryPrefs:   q.FurryPrefs,
		Roles:        q.Roles,
	}
}
