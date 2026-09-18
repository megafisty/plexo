package console

import "testing"

func TestNoticeSetAndClear(t *testing.T) {
	var n Notice
	if got := n.Text(); got != "" {
		t.Fatalf("zero notice = %q, want empty", got)
	}
	n.Set("boom")
	if got := n.Text(); got != "boom" {
		t.Fatalf("notice = %q, want boom", got)
	}
	n.Set("")
	if got := n.Text(); got != "" {
		t.Fatalf("cleared notice = %q, want empty", got)
	}
}

func TestNoticeNilSafe(t *testing.T) {
	var n *Notice
	n.Set("ignored")
	if got := n.Text(); got != "" {
		t.Fatalf("nil notice = %q, want empty", got)
	}
}
