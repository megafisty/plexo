// Character field mapping data: the lookup tables behind character profile
// fields (kinks, kink groups, infotags, infotag groups, and list values). The
// core fetches it once at startup and caches it in memory; it is never
// persisted. This is the REST counterpart to the channel catalog, which arrives
// over the chat socket.
package fchat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"

	"plexo/internal/model"
)

// DefaultMappingURL is the F-List character field mapping endpoint. It requires
// no authentication.
const DefaultMappingURL = "https://www.f-list.net/json/api/mapping-list.php"

// DefaultMappingFixturePath is the committed offline copy of the endpoint's
// response. The dev harness prefers it when present, so starting the core for
// testing does not touch the network; a deployed binary without the file
// fetches live.
const DefaultMappingFixturePath = "test/fixtures/mapping-list.json"

// maxMappingBody bounds a mapping response. The live response is ~130 KB; this
// leaves generous headroom without allowing an unbounded read.
const maxMappingBody = 8 << 20

// MappingFetcher loads the mapping data from the F-List HTTP endpoint.
type MappingFetcher struct {
	Client *http.Client
	URL    string
}

// Load posts to the endpoint (the documented method) and decodes the response.
func (f MappingFetcher) Load(ctx context.Context) (model.MappingList, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url(), http.NoBody)
	if err != nil {
		return model.MappingList{}, fmt.Errorf("fchat: mapping request: %w", err)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return model.MappingList{}, fmt.Errorf("fchat: mapping fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return model.MappingList{}, fmt.Errorf("fchat: mapping endpoint: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMappingBody))
	if err != nil {
		return model.MappingList{}, fmt.Errorf("fchat: mapping read: %w", err)
	}
	return DecodeMappingList(body)
}

// MappingFile loads the mapping data from a local JSON file: a committed
// fixture or a curl capture. It never touches the network.
type MappingFile struct{ Path string }

// Load reads and decodes the file.
func (f MappingFile) Load(context.Context) (model.MappingList, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return model.MappingList{}, fmt.Errorf("fchat: mapping read %s: %w", f.Path, err)
	}
	return DecodeMappingList(data)
}

// DecodeMappingList parses a raw mapping-list response.
func DecodeMappingList(data []byte) (model.MappingList, error) {
	var list model.MappingList
	if err := json.Unmarshal(data, &list); err != nil {
		return model.MappingList{}, fmt.Errorf("fchat: mapping decode: %w", err)
	}
	return list, nil
}

// BuildSearchMapping reduces the raw mapping tables to the shape the search UI
// renders: one SearchField per FKS filter, each holding only the options the UI
// shows and sends back. It is pure and runs once, before the manager caches the
// result, so the mapping endpoint never rebuilds it per request. Kink ids are
// parsed to numbers because FKS expects numbers; enum filters send their value
// strings. Kinks are sorted by name; enum values keep the server's order.
func BuildSearchMapping(list model.MappingList) model.SearchMapping {
	lists := make(map[string][]model.MappingListItem, len(list.ListItems))
	for _, item := range list.ListItems {
		lists[item.Name] = append(lists[item.Name], item)
	}

	kinks := make([]model.SearchEntry, 0, len(list.Kinks))
	for _, kink := range list.Kinks {
		id, err := strconv.Atoi(kink.ID)
		if err != nil {
			continue // not searchable without a numeric id
		}
		kinks = append(kinks, model.SearchEntry{Name: kink.Name, ID: id})
	}
	sort.Slice(kinks, func(i, j int) bool { return kinks[i].Name < kinks[j].Name })

	return model.SearchMapping{
		Kinks:        model.SearchField{Name: "Kinks", Field: "kinks", IDType: model.SearchIDNumber, Entries: kinks},
		Genders:      enumSearchField("Genders", "genders", lists["gender"]),
		Orientations: enumSearchField("Orientations", "orientations", lists["orientation"]),
		Languages:    enumSearchField("Languages", "languages", lists["languagepreference"]),
		FurryPrefs:   enumSearchField("Furry preferences", "furryprefs", lists["furrypref"]),
		Roles:        enumSearchField("Roles", "roles", lists["subdom"]),
	}
}

// enumSearchField builds a string-valued SearchField from one mapping-list list.
// The listitem value is both the label and the FKS value, so Name and ID match.
func enumSearchField(name, field string, items []model.MappingListItem) model.SearchField {
	entries := make([]model.SearchEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, model.SearchEntry{Name: item.Value, ID: item.Value})
	}
	return model.SearchField{Name: name, Field: field, IDType: model.SearchIDString, Entries: entries}
}

func (f MappingFetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

func (f MappingFetcher) url() string {
	if f.URL != "" {
		return f.URL
	}
	return DefaultMappingURL
}
