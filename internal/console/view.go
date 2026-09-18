package console

import "strings"

// Column separator between the sessions and clients panes. It is ASCII so the
// screen renders identically on any terminal.
const columnSeparator = " | "

// Layout renders the screen as exactly h lines, each at most w columns wide.
// It is pure so it can be tested without a terminal.
//
// The first lines are the product name and the UI address; below them the
// sessions and clients panes sit side by side; the last line is the key
// legend and the line above it shows the most recent error. Very small
// terminals degrade to the header and legend only.
func Layout(w, h int, s State) []string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		return nil
	}

	// The last line is the key legend; the line above it shows the most recent
	// error (blank when there is none). Everything else is the body.
	body := h - 2
	if body < 0 {
		body = 0
	}

	lines := make([]string, 0, h)
	push := func(line string) {
		if len(lines) < body {
			lines = append(lines, truncate(line, w))
		}
	}

	push("Plexo")
	push(s.URL)
	push("")

	leftW, rightW := columns(w)
	if leftW >= 1 && rightW >= 1 {
		push(twoCol(leftW, rightW, "Characters", "Clients"))
		push(twoCol(leftW, rightW, strings.Repeat("-", leftW), strings.Repeat("-", rightW)))
		for i := 0; len(lines) < body; i++ {
			var left, right string
			if i < len(s.Sessions) {
				left = s.Sessions[i]
			}
			if i < len(s.Clients) {
				right = s.Clients[i]
			}
			push(twoCol(leftW, rightW, left, right))
		}
	}

	for len(lines) < body {
		lines = append(lines, "")
	}

	if h == 1 {
		return append(lines, truncate("q quit    b open in browser", w))
	}
	lines = append(lines, truncate(s.Notice, w))
	lines = append(lines, truncate("q quit    b open in browser", w))
	return lines
}

// columns splits the content width into the two panes and the separator.
func columns(w int) (leftW, rightW int) {
	leftW = (w - len(columnSeparator)) / 2
	if leftW < 1 {
		leftW = 1
	}
	rightW = w - len(columnSeparator) - leftW
	if rightW < 1 {
		rightW = 1
	}
	return leftW, rightW
}

// twoCol lays out the left and right cells on one line.
func twoCol(leftW, rightW int, left, right string) string {
	line := pad(truncate(left, leftW), leftW) + columnSeparator + truncate(right, rightW)
	return strings.TrimRight(line, " ")
}

// truncate shortens s to at most w runes.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w])
}

// pad appends spaces to s until it is exactly w runes long.
func pad(s string, w int) string {
	n := w - len([]rune(s))
	if n <= 0 {
		return s
	}
	return s + strings.Repeat(" ", n)
}
