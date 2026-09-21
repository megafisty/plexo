// Package fixtures holds committed offline copies of external API responses so
// tests and the dev harness never depend on the live network.
package fixtures

import _ "embed"

//go:embed mapping-list.json
var mappingList []byte

// MappingList returns the raw JSON of the F-List character field mapping-list
// response, captured once with curl from
// https://www.f-list.net/json/api/mapping-list.php. It is decoded by
// internal/fchat.
func MappingList() []byte { return mappingList }

//go:embed friend-bookmark-lists.json
var friendBookmarkLists []byte

// FriendBookmarkLists returns the raw JSON of the F-List combined
// friend-bookmark-lists response. The shape mirrors the documented example in
// docs/fchat/API.html; it is decoded by internal/fchat.
func FriendBookmarkLists() []byte { return friendBookmarkLists }
