package core_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/session"
	"plexo/test/fakeserver"
	"plexo/test/fchatpipe"
)

// newSearchHarness builds a harness whose fake server answers FKS with the
// given options.
func newSearchHarness(t *testing.T, opts fakeserver.Options) *harness {
	t.Helper()
	opts.Character = char
	fac := fakeserver.NewWSFactory(opts)
	t.Cleanup(fac.Close)
	return newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(fac.Dial)
	}, fac)
}

// waitServerFKS waits for the outbound FKS and returns its decoded payload.
func waitServerFKS(t *testing.T, srv *fakeserver.Server) fchat.FKSRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case cmd := <-srv.Received():
			if cmd.Code != "FKS" {
				continue
			}
			req, err := fchat.Decode[fchat.FKSRequest](cmd)
			if err != nil {
				t.Fatalf("decode FKS: %v", err)
			}
			return req
		default:
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("server never received FKS")
	return fchat.FKSRequest{}
}

// TestSearchRelaysResults: a POST-triggered FKS reaches the server with the
// filter set, its reply is cached on the session, and only a notice is emitted.
// The enriched rows are pulled with SearchResults.
func TestSearchRelaysResults(t *testing.T) {
	h := newSearchHarness(t, fakeserver.Options{
		SearchResults: []string{"Some Guy", "Some Gal"},
		// An online friend gives a post-LIS friends event to wait on; the
		// client no longer receives offline friends.
		Friends: []string{"Some Guy"},
		Roster: [][]string{
			{"Some Guy", "Male", "looking", "hi"},
			{"Some Gal", "Female", "online", ""},
		},
	})
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)
	// Wait for FRL (sent after LIS) so the roster is hydrated before searching.
	if _, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		_, ok := stateValue[model.FriendsPayload](ev, model.AccountKey("friends"))
		return ok
	}); !ok {
		t.Fatalf("roster never hydrated; events: %s", dump(h.ui))
	}

	res, err := h.mgr.Search(char, model.SearchQuery{Kinks: []int{523}, Genders: []string{"Male"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("search rejected: %+v", res)
	}

	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		_, ok := nsValue[model.SearchNotice](ev, model.StateSearch)
		return ok
	})
	if !ok {
		t.Fatalf("no search event; events: %s", dump(h.ui))
	}
	notice, isN := nsValue[model.SearchNotice](ev, model.StateSearch)
	if !isN || notice.Revision == 0 {
		t.Fatalf("search payload = %#v, want a notice with a revision", ev.Payload)
	}
	// The rows live in the session's cache, not the event.
	cached, err := h.mgr.SearchResults(char)
	if err != nil {
		t.Fatalf("search results: %v", err)
	}
	if cached.Revision != notice.Revision {
		t.Fatalf("cached revision = %d, want %d", cached.Revision, notice.Revision)
	}
	want := []model.MemberInfo{
		{Name: "Some Guy", Gender: "Male", Status: "looking", StatusMsg: "hi", Online: true},
		{Name: "Some Gal", Gender: "Female", Status: "online", Online: true},
	}
	if !reflect.DeepEqual(cached.Characters, want) {
		t.Fatalf("characters = %#v, want %#v", cached.Characters, want)
	}

	req := waitServerFKS(t, h.fac.First())
	if !reflect.DeepEqual(req.Kinks, []int{523}) || !reflect.DeepEqual(req.Genders, []string{"Male"}) {
		t.Fatalf("outbound FKS = %+v", req)
	}
}

// TestSearchEmptyResultIsEventNotError: ERR 18 is a successful empty result, so
// it becomes an empty search event and never an error.
func TestSearchEmptyResultIsEventNotError(t *testing.T) {
	h := newSearchHarness(t, fakeserver.Options{SearchErr: 18})
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	res, err := h.mgr.Search(char, model.SearchQuery{Kinks: []int{}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("search rejected: %+v", res)
	}

	ev, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		_, ok := nsValue[model.SearchNotice](ev, model.StateSearch)
		return ok
	})
	if !ok {
		t.Fatalf("no search event; events: %s", dump(h.ui))
	}
	p, isP := nsValue[model.SearchNotice](ev, model.StateSearch)
	if !isP || p.Revision == 0 {
		t.Fatalf("search payload = %#v, want a notice with a revision", ev.Payload)
	}
	cached, err := h.mgr.SearchResults(char)
	if err != nil {
		t.Fatalf("search results: %v", err)
	}
	if cached.Characters == nil || len(cached.Characters) != 0 {
		t.Fatalf("cached characters = %#v, want empty non-nil", cached.Characters)
	}
	for _, e := range h.ui.Events() {
		if e.Kind == model.EvError {
			t.Fatalf("ERR 18 produced an error event: %+v", e)
		}
	}
}

// TestSearchErrorSurfacesAsError: a true search failure (throttle/too many) is
// surfaced through the generic error event and yields no search event.
func TestSearchErrorSurfacesAsError(t *testing.T) {
	h := newSearchHarness(t, fakeserver.Options{SearchErr: 50})
	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	h.waitLive(t)

	res, err := h.mgr.Search(char, model.SearchQuery{Kinks: []int{}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("search rejected: %+v", res)
	}

	_, ok := h.ui.WaitFor(2*time.Second, func(ev model.Event) bool {
		p, isErr := ev.Payload.(model.ErrorPayload)
		return ev.Kind == model.EvError && isErr && p.Code == 50
	})
	if !ok {
		t.Fatalf("no error event; events: %s", dump(h.ui))
	}
	for _, e := range h.ui.Events() {
		if e.Kind == model.EvState && model.KeyNamespace(e.Payload.(model.StatePayload).Key) == model.StateSearch {
			t.Fatalf("failed search produced a search event: %+v", e)
		}
	}
}

// TestSearchRejectedUntilReady: FKS is only sent once the session is ready; a
// connecting session reports not_ready instead of queueing a doomed command.
func TestSearchRejectedUntilReady(t *testing.T) {
	h := newHarnessWithDial(t, model.InterestSummary, func(string) session.Dialer {
		return session.Dialer(func(ctx context.Context) (fchat.Conn, error) {
			// The server side never answers, so the session stays in the
			// idn_sent phase.
			client, _ := fchatpipe.Pipe()
			return client, nil
		})
	}, nil)

	if err := h.mgr.Login(acct, char); err != nil {
		t.Fatal(err)
	}
	res, err := h.mgr.Search(char, model.SearchQuery{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Accepted || res.ErrorCode != "not_ready" {
		t.Fatalf("res = %+v, want not_ready", res)
	}
}
