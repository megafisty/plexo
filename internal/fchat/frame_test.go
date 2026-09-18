package fchat

import "testing"

func TestParseMarshalRoundTrip(t *testing.T) {
	cases := []struct {
		raw  string
		code string
		data string
	}{
		{`MSG {"channel":"Frontpage","message":"hi"}`, "MSG", `{"channel":"Frontpage","message":"hi"}`},
		{`UPT`, "UPT", ""},
		{`IDN {"character":"Vix"}`, "IDN", `{"character":"Vix"}`},
	}
	for _, tc := range cases {
		got, err := Parse([]byte(tc.raw))
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.raw, err)
		}
		if got.Code != tc.code || string(got.Data) != tc.data {
			t.Fatalf("Parse(%q) = %q/%q, want %q/%q", tc.raw, got.Code, got.Data, tc.code, tc.data)
		}
		if string(got.Marshal()) != tc.raw {
			t.Fatalf("Marshal round trip = %q, want %q", got.Marshal(), tc.raw)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, raw := range []string{"", "AB", "abc {}", "MSGX {}", "12! {}"} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", raw)
		}
	}
}

func TestUnknownFrameIsWellFormed(t *testing.T) {
	cmd, err := Parse([]byte(`XYZ {"data":"foo"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cmd.Code != "XYZ" {
		t.Fatalf("code = %q", cmd.Code)
	}
}
