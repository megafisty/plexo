package model

import "testing"

// TestConvRefFromKey covers the parser that recovered the scope a conv/summary/
// typing payload no longer repeats: the broker's interest gate and the client's
// apply both depend on it reading the same ref the key was built from.
func TestConvRefFromKey(t *testing.T) {
	cases := []struct {
		key  string
		want ConvRef
		ok   bool
	}{
		{ConvKey("Vix", ConvRef{Kind: ConvOfficial, ID: "Frontpage"}), ConvRef{Kind: ConvOfficial, ID: "Frontpage"}, true},
		{SummaryKey("Vix", ConvRef{Kind: ConvDM, ID: "Kira"}), ConvRef{Kind: ConvDM, ID: "Kira"}, true},
		{TypingKey("Vix", ConvRef{Kind: ConvRoom, ID: "ADH-abc"}, "Kira"), ConvRef{Kind: ConvRoom, ID: "ADH-abc"}, true},
		{ConvKey("Vix", ConvRef{Kind: ConvDM, ID: ""}), ConvRef{Kind: ConvDM, ID: ""}, true},
		{"account/friends", ConvRef{}, false},
		{"session/Vix", ConvRef{}, false},
		{"character/Kira", ConvRef{}, false},
		{"conv/Vix/", ConvRef{}, false},
		{"conv/Vix/nokind", ConvRef{}, false},
	}
	for _, tc := range cases {
		got, ok := ConvRefFromKey(tc.key)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ConvRefFromKey(%q) = (%+v, %v), want (%+v, %v)", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}
