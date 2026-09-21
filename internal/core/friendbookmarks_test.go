package core_test

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"plexo/internal/core"
)

// fakeFriendBookmarkLister is a FriendBookmarkLister whose behaviour is driven
// per call, so tests can assert fetch dedupe, retry, and reset.
type fakeFriendBookmarkLister struct {
	mu      sync.Mutex
	calls   int
	friends []string
	marks   []string
	errs    []error // errs[i] is returned on call i+1; missing entries are nil
}

func (f *fakeFriendBookmarkLister) FetchFriendBookmarkLists(context.Context) ([]string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, nil, f.errs[i]
	}
	return f.friends, f.marks, nil
}

func (f *fakeFriendBookmarkLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func waitSplit(t *testing.T, mgr *core.Manager, wantFriends, wantBookmarks int) ([]string, []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		friends, bookmarks := mgr.FriendBookmarks()
		if len(friends) == wantFriends && len(bookmarks) == wantBookmarks {
			return friends, bookmarks
		}
		if time.Now().After(deadline) {
			t.Fatalf("split not populated: friends=%v bookmarks=%v", friends, bookmarks)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFriendBookmarkFetchIsSharedPerCohort: the first ready session fetches the
// split once; a second concurrent session reuses it.
func TestFriendBookmarkFetchIsSharedPerCohort(t *testing.T) {
	fake := &fakeFriendBookmarkLister{friends: []string{"Aiaru"}, marks: []string{"Carol"}}
	mgr := core.NewManager(context.Background(), core.Config{FriendBookmarks: fake})

	mgr.SessionReady("Vix")
	mgr.SessionReady("Bob")
	waitSplit(t, mgr, 1, 1)
	if got := fake.callCount(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

// TestFriendBookmarkResetOnLastGone: when the last session leaves, the flag
// resets so the next cohort refetches.
func TestFriendBookmarkResetOnLastGone(t *testing.T) {
	fake := &fakeFriendBookmarkLister{friends: []string{"Aiaru"}, marks: []string{"Carol"}}
	mgr := core.NewManager(context.Background(), core.Config{FriendBookmarks: fake})

	mgr.SessionReady("Vix")
	mgr.SessionReady("Bob")
	waitSplit(t, mgr, 1, 1)

	// One session leaving does not reset while another is ready.
	mgr.SessionGone("Vix")
	if friends, _ := mgr.FriendBookmarks(); len(friends) != 1 {
		t.Fatalf("split cleared while a session was still ready: %v", friends)
	}

	mgr.SessionGone("Bob")
	if friends, bookmarks := mgr.FriendBookmarks(); len(friends) != 0 || len(bookmarks) != 0 {
		t.Fatalf("split not cleared after last session left: %v %v", friends, bookmarks)
	}

	mgr.SessionReady("Bob")
	waitSplit(t, mgr, 1, 1)
	if got := fake.callCount(); got != 2 {
		t.Fatalf("fetch calls = %d, want 2 (refetch after reset)", got)
	}
}

// TestFriendBookmarkFetchFailureRetries: a failed fetch leaves the flag unset,
// so a later ready session in the same cohort retries.
func TestFriendBookmarkFetchFailureRetries(t *testing.T) {
	fake := &fakeFriendBookmarkLister{
		friends: []string{"Aiaru"},
		marks:   []string{"Carol"},
		errs:    []error{context.DeadlineExceeded},
	}
	mgr := core.NewManager(context.Background(), core.Config{FriendBookmarks: fake})

	mgr.SessionReady("Vix") // fails
	// Once the failed fetch releases the in-flight guard, a later ready session
	// retries. Distinct names sidestep the ready-set dedupe while we wait.
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; fake.callCount() < 2; i++ {
		mgr.SessionReady("later-" + strconv.Itoa(i))
		if time.Now().After(deadline) {
			t.Fatalf("fetch calls = %d, want a retry", fake.callCount())
		}
		time.Sleep(time.Millisecond)
	}
	waitSplit(t, mgr, 1, 1)
}

// TestApplyFriendBookmarkRTB: realtime-bridge deltas fold into the cached split.
func TestApplyFriendBookmarkRTB(t *testing.T) {
	mgr := core.NewManager(context.Background(), core.Config{FriendBookmarks: &fakeFriendBookmarkLister{}})

	mgr.ApplyFriendBookmarkRTB("friendadd", "Bob")
	mgr.ApplyFriendBookmarkRTB("trackadd", "Carol")
	friends, bookmarks := mgr.FriendBookmarks()
	if !slices.Contains(friends, "Bob") || slices.Contains(friends, "Carol") {
		t.Fatalf("friends = %v, want [Bob]", friends)
	}
	if !slices.Contains(bookmarks, "Carol") || slices.Contains(bookmarks, "Bob") {
		t.Fatalf("bookmarks = %v, want [Carol]", bookmarks)
	}

	mgr.ApplyFriendBookmarkRTB("friendremove", "Bob")
	mgr.ApplyFriendBookmarkRTB("trackrem", "Carol")
	friends, bookmarks = mgr.FriendBookmarks()
	if len(friends) != 0 || len(bookmarks) != 0 {
		t.Fatalf("split not cleared: %v %v", friends, bookmarks)
	}
}

// TestContactsReportsFetchedSplit: Contacts exposes the split and flips Fetched
// only once the REST fetch lands.
func TestContactsReportsFetchedSplit(t *testing.T) {
	fake := &fakeFriendBookmarkLister{friends: []string{"Aiaru"}, marks: []string{"Carol"}}
	mgr := core.NewManager(context.Background(), core.Config{FriendBookmarks: fake})

	if cs := mgr.Contacts(); cs.Fetched || len(cs.Friends) != 0 || len(cs.Bookmarks) != 0 {
		t.Fatalf("pre-fetch contacts = %+v, want empty and unfetched", cs)
	}
	mgr.SessionReady("Vix")
	waitSplit(t, mgr, 1, 1)

	cs := mgr.Contacts()
	if !cs.Fetched || !cs.Friends["aiaru"] || !cs.Bookmarks["carol"] {
		t.Fatalf("contacts = %+v", cs)
	}
}

// TestSetBookmarkAppliesAndNotifies: a client-applied bookmark lands in the
// split immediately, independent of any RTB.
func TestSetBookmarkAppliesAndNotifies(t *testing.T) {
	fake := &fakeFriendBookmarkLister{friends: []string{"Aiaru"}}
	mgr := core.NewManager(context.Background(), core.Config{FriendBookmarks: fake})
	mgr.SessionReady("Vix")
	waitSplit(t, mgr, 1, 0)

	mgr.SetBookmark("Carol", true)
	friends, bookmarks := mgr.FriendBookmarks()
	if slices.Contains(friends, "Carol") || !slices.Contains(bookmarks, "Carol") {
		t.Fatalf("after add: friends=%v bookmarks=%v", friends, bookmarks)
	}
	mgr.SetBookmark("Carol", false)
	friends, bookmarks = mgr.FriendBookmarks()
	if slices.Contains(friends, "Carol") || slices.Contains(bookmarks, "Carol") {
		t.Fatalf("after remove: friends=%v bookmarks=%v", friends, bookmarks)
	}
}
