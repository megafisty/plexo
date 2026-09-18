package console

import (
	"strings"
	"testing"
)

func TestLayoutShape(t *testing.T) {
	const w, h = 80, 24
	lines := Layout(w, h, State{
		URL:      "http://192.168.1.20:8080",
		Sessions: []string{"Alice", "Bob"},
		Clients:  []string{"192.168.1.5:51234"},
	})
	if len(lines) != h {
		t.Fatalf("got %d lines, want %d", len(lines), h)
	}
	for i, line := range lines {
		if n := len([]rune(line)); n > w {
			t.Fatalf("line %d is %d runes, want <= %d: %q", i, n, w, line)
		}
	}
	if lines[0] != "Plexo" {
		t.Fatalf("first line = %q, want Plexo", lines[0])
	}
	if lines[1] != "http://192.168.1.20:8080" {
		t.Fatalf("second line = %q, want the URL", lines[1])
	}
	body := strings.Join(lines, "\n")
	for _, want := range []string{"Characters", "Clients", "Alice", "Bob", "192.168.1.5:51234"} {
		if !strings.Contains(body, want) {
			t.Fatalf("screen missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(lines[h-1], "q quit") || !strings.Contains(lines[h-1], "b open") {
		t.Fatalf("footer = %q, want key legend", lines[h-1])
	}
}

func TestLayoutColumnsAlign(t *testing.T) {
	lines := Layout(80, 24, State{Sessions: []string{"Alice"}, Clients: []string{"host:1"}})
	var header, row string
	for _, line := range lines {
		if strings.Contains(line, "Characters") {
			header = line
		}
		if strings.Contains(line, "Alice") {
			row = line
		}
	}
	if header == "" || row == "" {
		t.Fatalf("missing header or row:\n%s", strings.Join(lines, "\n"))
	}
	// The separator must sit at the same column in every row so the panes line
	// up.
	if hi, ri := strings.Index(header, columnSeparator), strings.Index(row, columnSeparator); hi != ri {
		t.Fatalf("separator column differs: header %d, row %d", hi, ri)
	}
}

func TestLayoutTruncatesLongCells(t *testing.T) {
	long := strings.Repeat("x", 500)
	lines := Layout(40, 10, State{Sessions: []string{long}, Clients: []string{long}})
	for _, line := range lines {
		if len([]rune(line)) > 40 {
			t.Fatalf("line too wide: %q", line)
		}
	}
}

func TestLayoutNotice(t *testing.T) {
	const w, h = 60, 12
	withNotice := Layout(w, h, State{URL: "http://x:1", Notice: "render reload: boom"})
	if got := withNotice[h-2]; got != "render reload: boom" {
		t.Fatalf("notice line = %q", got)
	}
	if !strings.Contains(withNotice[h-1], "q quit") {
		t.Fatalf("footer moved: %q", withNotice[h-1])
	}

	without := Layout(w, h, State{URL: "http://x:1"})
	if without[h-2] != "" {
		t.Fatalf("empty notice line = %q, want blank", without[h-2])
	}
}

func TestLayoutTiny(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {3, 2}, {5, 5}, {0, 0}, {-1, 4}} {
		lines := Layout(size[0], size[1], State{URL: "http://localhost:1", Sessions: []string{"A"}})
		if size[1] > 0 && len(lines) != size[1] {
			t.Fatalf("size %v: got %d lines", size, len(lines))
		}
	}
}
