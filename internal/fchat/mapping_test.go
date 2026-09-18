package fchat_test

// Every test here is offline: the fetcher is exercised against httptest and the
// remaining cases use a temp file or the committed fixture. No test ever
// constructs a Fetcher pointed at the live F-List endpoint.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/test/fixtures"
)

const sample = `{
  "kinks": [{"id":"1","name":"Dirty Talking","description":"...","group_id":"42"}],
  "kink_groups": [{"id":"42","name":"Verbal"}],
  "infotags": [{"id":"3","name":"Gender","type":"list","list":"gender","group_id":"3"}],
  "infotag_groups": [{"id":"3","name":"Basics"}],
  "listitems": [{"id":"1","name":"gender","value":"Male"}]
}`

func TestFetcher(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()

	list, err := fchat.MappingFetcher{URL: srv.URL, Client: srv.Client()}.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %s, want POST", method)
	}
	if len(list.Kinks) != 1 || list.Kinks[0].Name != "Dirty Talking" {
		t.Fatalf("kinks = %+v", list.Kinks)
	}
	if len(list.Infotags) != 1 || list.Infotags[0].List != "gender" {
		t.Fatalf("infotags = %+v", list.Infotags)
	}
	if len(list.ListItems) != 1 || list.ListItems[0].Value != "Male" {
		t.Fatalf("listitems = %+v", list.ListItems)
	}
}

func TestFetcherHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := (fchat.MappingFetcher{URL: srv.URL, Client: srv.Client()}).Load(context.Background()); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapping.json")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := fchat.MappingFile{Path: path}.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list.KinkGroups) != 1 || list.KinkGroups[0].Name != "Verbal" {
		t.Fatalf("kink groups = %+v", list.KinkGroups)
	}
}

func TestFileMissing(t *testing.T) {
	_, err := fchat.MappingFile{Path: filepath.Join(t.TempDir(), "absent.json")}.Load(context.Background())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want a not-exist error", err)
	}
}

func TestDecodeInvalid(t *testing.T) {
	if _, err := fchat.DecodeMappingList([]byte("{")); err == nil {
		t.Fatal("expected a decode error")
	}
}

// TestBuildSearchMapping checks the raw mapping tables reduce to the search
// shape the UI consumes: one field per FKS filter, with the right ids, labels,
// and FKS payload keys.
func TestBuildSearchMapping(t *testing.T) {
	list, err := fchat.DecodeMappingList(fixtures.MappingList())
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	m := fchat.BuildSearchMapping(list)

	for _, f := range []model.SearchField{m.Kinks, m.Genders, m.Orientations, m.Languages, m.FurryPrefs, m.Roles} {
		if f.Name == "" || f.Field == "" || f.IDType == "" || len(f.Entries) == 0 {
			t.Fatalf("incomplete field: %+v", f)
		}
	}
	if m.Kinks.Field != "kinks" || m.Kinks.IDType != model.SearchIDNumber {
		t.Fatalf("kinks field = %+v", m.Kinks)
	}
	// Kink ids must be numbers (FKS expects numbers) and sorted by name.
	for i, e := range m.Kinks.Entries {
		if _, ok := e.ID.(int); !ok {
			t.Fatalf("kink %q id is %T, want int", e.Name, e.ID)
		}
		if i > 0 && m.Kinks.Entries[i-1].Name > e.Name {
			t.Fatalf("kinks not sorted: %q before %q", m.Kinks.Entries[i-1].Name, e.Name)
		}
	}
	// Enum entries use the value string as both label and FKS value.
	found := false
	for _, e := range m.Genders.Entries {
		if e.ID == nil {
			t.Fatalf("gender entry has nil id: %+v", e)
		}
		if v, ok := e.ID.(string); !ok || v != e.Name {
			t.Fatalf("gender entry %+v: id must match name", e)
		}
		if e.Name == "Male" {
			found = true
		}
	}
	if !found {
		t.Error(`genders missing "Male"`)
	}
}

// TestDecodeFixture checks the committed curl capture decodes and carries the
// gender and orientation lists the client needs for search.
func TestDecodeFixture(t *testing.T) {
	list, err := fchat.DecodeMappingList(fixtures.MappingList())
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(list.Kinks) == 0 || len(list.Infotags) == 0 || len(list.ListItems) == 0 {
		t.Fatalf("fixture is missing data: kinks=%d infotags=%d listitems=%d",
			len(list.Kinks), len(list.Infotags), len(list.ListItems))
	}
	lists := map[string]int{}
	for _, it := range list.ListItems {
		lists[it.Name]++
	}
	for _, want := range []string{"gender", "orientation"} {
		if lists[want] == 0 {
			t.Errorf("fixture has no %q list items", want)
		}
	}
}
