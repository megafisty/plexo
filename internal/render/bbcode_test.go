package render

import (
	"os"
	"path/filepath"
	"testing"
)

func render(t *testing.T, s *Renderer, body string) string {
	t.Helper()
	h, err := s.Render(body)
	if err != nil {
		t.Fatalf("Render(%q): %v", body, err)
	}
	return h
}

func TestRender(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"bold", "[b]hi[/b]", "<b>hi</b>"},
		{"nested", "[b][i]x[/i][/b]", "<b><i>x</i></b>"},
		// noparse emits its content as HTML-escaped literal source; only the
		// first close ends the run and params are ignored. It is the one tag
		// whose content is never parsed, and the behavior is hardcoded in the
		// parser rather than selectable from the table.
		{"noparse", "[noparse][b]hi[/b][/noparse]", `<code class="bc-noparse">[b]hi[/b]</code>`},
		{"noparse_wrapped", "[s][noparse][i][b]hello[/b][/i][/noparse][/s]", `<s><code class="bc-noparse">[i][b]hello[/b][/i]</code></s>`},
		{"noparse_escapes_html", "[noparse]<b>&\"x[/noparse]", `<code class="bc-noparse">&lt;b&gt;&amp;&#34;x</code>`},
		{"noparse_wire_entities", "[noparse]a &gt; b &amp;lt; c[/noparse]", `<code class="bc-noparse">a &gt; b &amp;lt; c</code>`},
		{"noparse_param_ignored", "[noparse=x][b]y[/b][/noparse]", `<code class="bc-noparse">[b]y[/b]</code>`},
		{"noparse_empty", "[noparse][/noparse]", `<code class="bc-noparse"></code>`},
		{"noparse_first_close_wins", "[noparse]a[/noparse]b[/noparse]", `<code class="bc-noparse">a</code>b[/noparse]`},
		{"noparse_nested_in_tag", "[b][noparse][i]x[/i][/noparse][/b]", `<b><code class="bc-noparse">[i]x[/i]</code></b>`},
		{"noparse_unclosed", "[noparse]a[b]c[/b]", "[noparse]a[b]c[/b]"},
		{"spoiler", "[spoiler]secret[/spoiler]", `<div class="bc-spoiler"><div class="bc-spoiler-body">secret</div></div>`},
		{"spoiler_nested", "[spoiler]a[b]b[/b][spoiler]c[/spoiler][/spoiler]", `<div class="bc-spoiler"><div class="bc-spoiler-body">a<b>b</b><div class="bc-spoiler"><div class="bc-spoiler-body">c</div></div></div></div>`},
		{"spoiler_unclosed", "[spoiler]a[b]b[/b]", "[spoiler]a[b]b[/b]"},
		{"session", "[session=Smol Preds]adh-86ea949691ac9d6343eb[/session]", `<span class="bc-session" data-conv-kind="room" data-conv-id="adh-86ea949691ac9d6343eb">Smol Preds</span>`},
		{"session_upper_id", "[session=T]ADH-ABC[/session]", `<span class="bc-session" data-conv-kind="room" data-conv-id="ADH-ABC">T</span>`},
		{"session_blank_label", "[session]adh-abc[/session]", `<span class="bc-session" data-conv-kind="room" data-conv-id="adh-abc"></span>`},
		{"session_bad_content", "[session=T]not adh![/session]", "[session=T]not adh![/session]"},
		{"session_unclosed", "[session=T]adh-abc", "[session=T]adh-abc"},
		{"escape", "<script>&\"", "&lt;script&gt;&amp;&#34;"},
		{"void", "a[br]b", "a<br>b"},
		{"selfparam", "[color=red]x[/color]", "<span style=\"color:red\">x</span>"},
		{"color_hex", "[color=#ff0000]x[/color]", "<span style=\"color:#ff0000\">x</span>"},
		{"color_bad", "[color=expr(x)]y[/color]", "[color=expr(x)]y[/color]"},
		{"url", "[url=http://x/y]t[/url]", "<a href=\"http://x/y\" target=\"_blank\" rel=\"noopener noreferrer\">t</a>"},
		{"url_bad_scheme", "[url=javascript:alert(1)]t[/url]", "[url=javascript:alert(1)]t[/url]"},
		{"url_no_param", "[url]http://x/y[/url]", "[url]http://x/y[/url]"},
		{"url_amp", "[url=http://x?a=1&amp;b=2]t[/url]", "<a href=\"http://x?a=1&amp;b=2\" target=\"_blank\" rel=\"noopener noreferrer\">t</a>"},
		// The wire is HTML-escaped by the server; decode it once. A literal '>'
		// must round-trip to a displayed '>', and a typed "&lt;" must stay
		// literal (single-level decode).
		{"wire_entities", "a &gt; b &lt; c &amp; d", "a &gt; b &lt; c &amp; d"},
		{"wire_entities_once", "&amp;lt;", "&amp;lt;"},
		{"eicon", "[eicon]bloop[/eicon]", `<img class="bc-eicon" src="https://static.f-list.net/images/eicon/bloop.png" alt="bloop">`},
		// Slug tags fold content to lowercase: F-List image URLs are lowercase
		// and case-sensitive, but users type names in any case.
		{"eicon_lower", "[eicon]Bloop[/eicon]", `<img class="bc-eicon" src="https://static.f-list.net/images/eicon/bloop.png" alt="bloop">`},
		{
			"icon_lower",
			"[icon]SomeName[/icon]",
			`<a href="https://www.f-list.net/c/somename" target="_blank" rel="noopener noreferrer"><img class="bc-avatar" src="https://static.f-list.net/images/avatar/somename.png" alt="somename"></a>`,
		},
		// user is not transformed: its content is visible link text, so case is
		// preserved (the profile URL is case-insensitive).
		{
			"user_keeps_case",
			"[user]Alice[/user]",
			`<a href="https://www.f-list.net/c/Alice" target="_blank" rel="noopener noreferrer">Alice</a>`,
		},
		{"eicon_bad_chars", "[eicon]a\"b[/eicon]", "[eicon]a&#34;b[/eicon]"},
		{"eicon_empty", "[eicon][/eicon]", "[eicon][/eicon]"},
		// A failed guard recovers like an unknown tag: only the opening tag is
		// literal, its content is still parsed, and the consumed close is
		// re-emitted. Bad params and bad content must agree, so neither swallows a
		// subtree.
		{"param_bad_keeps_nested", "[color=#zz]a[b]c[/b]d[/color]", "[color=#zz]a<b>c</b>d[/color]"},
		{"content_bad_keeps_nested", "[eicon]a[b]c[/b]d\"x[/eicon]", "[eicon]a<b>c</b>d&#34;x[/eicon]"},
		{"content_bad_keeps_nested_wrap", "[user]a[b]c[/b]d[/user]", "[user]a<b>c</b>d[/user]"},
		{"content_bad_unclosed", "[eicon]a[b]c[/b]d\"x", "[eicon]a<b>c</b>d&#34;x"},
		{"img_absent", "[img]http://x/y[/img]", "[img]http://x/y[/img]"},
		// Malformed constructs become literal source, and parsing resumes inside
		// them, so nested valid markup and later siblings still render.
		{"unknown_keeps_nested", "a[nope][b]x[/b][/nope]z", "a[nope]<b>x</b>[/nope]z"},
		{"unknown_nested_same", "[x]a[x]b[/x]c[/x]d", "[x]a[x]b[/x]c[/x]d"},
		{"unmatched_open", "[b]oops", "[b]oops"},
		{"stray_close", "a[/b]c", "a[/b]c"},
		// A stray close inside a valid tag is literal text; the tag still closes
		// and later siblings still render.
		{"mismatched_close_recovers", "[b]x[/i][/b] [i]y[/i]", "<b>x[/i]</b> <i>y</i>"},
		// An unclosed outer tag dumps its whole remaining source, so nothing
		// inside it is half-rendered.
		{"mismatched_close_unclosed", "[b]x[/i][i]y[/i]", "[b]x[/i][i]y[/i]"},
		{"recover_after_bad", "[color=#zz]x[/color] [b]ok[/b]", "[color=#zz]x[/color] <b>ok</b>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, s, tc.in); got != tc.want {
				t.Fatalf("Render(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapesGuardBoundary locks in that a value can pass a guard (scheme
// prefix, name charset) and still be HTML-escaped before interpolation.
func TestEscapesGuardBoundary(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{
			"[url=http://x/\"><script>]t[/url]",
			"<a href=\"http://x/&#34;&gt;&lt;script&gt;\" target=\"_blank\" rel=\"noopener noreferrer\">t</a>",
		},
		{
			`[color=red]a"b[/color]`,
			// Param passes, in-content quotes are still escaped.
			`<span style="color:red">a&#34;b</span>`,
		},
	}
	for _, tc := range cases {
		if got := render(t, s, tc.in); got != tc.want {
			t.Fatalf("Render(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestContentDuplication exercises the shapeMulti path: [eicon] interpolates the
// rendered content twice.
func TestContentDuplication(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, s, "[eicon]cat[/eicon]")
	want := `<img class="bc-eicon" src="https://static.f-list.net/images/eicon/cat.png" alt="cat">`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCache(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	a := render(t, s, "[b]x[/b]")
	b := render(t, s, "[b]x[/b]")
	if a != b {
		t.Fatalf("cache mismatch: %q vs %q", a, b)
	}
}

// TestRenderMessage covers the client emote convention: a leading "/me" token
// is dropped so the action flows after the inline speaker, and everything else
// gains a ": " separator so it reads "Name: text".
func TestRenderMessage(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello", ": hello"},
		{"markup", "[b]hi[/b]", ": <b>hi</b>"},
		{"emote", "/me waves", " waves"},
		{"emote_markup", "/me [b]waves[/b]", " <b>waves</b>"},
		{"emote_bare", "/me", ""},
		{"emote_tab", "/me\twaves", "\twaves"},
		{"not_emote_prefix", "/merry xmas", ": /merry xmas"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.RenderMessage(tc.in)
			if err != nil {
				t.Fatalf("RenderMessage(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("RenderMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRenderMessageCacheSeparation: a status body and a message body with the
// same source must not share a cache entry, since the message carries the ": "
// separator and the status does not.
func TestRenderMessageCacheSeparation(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// Render the status first so the message path cannot merely overwrite it.
	if got := render(t, s, "hi"); got != "hi" {
		t.Fatalf("Render(hi) = %q, want %q", got, "hi")
	}
	got, err := s.RenderMessage("hi")
	if err != nil {
		t.Fatal(err)
	}
	if got != ": hi" {
		t.Fatalf("RenderMessage(hi) = %q, want %q", got, ": hi")
	}
}

func writeTable(t *testing.T, path, valid string) {
	t.Helper()
	src := `{"tags":{"x":{"valid":"` + valid + `"}}}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "table.json")
	writeTable(t, path, "A:{content}")

	s, err := newWithPaths(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, s, "[x]z[/x]"); got != "A:z" {
		t.Fatalf("before reload = %q", got)
	}
	// Prime the cache under the old table.
	_ = render(t, s, "[x]cached[/x]")

	writeTable(t, path, "B:{content}")
	if err := s.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := render(t, s, "[x]z[/x]"); got != "B:z" {
		t.Fatalf("after reload = %q", got)
	}
	// Cache must have been cleared: the old table's result is gone.
	if got := render(t, s, "[x]cached[/x]"); got != "B:cached" {
		t.Fatalf("cache not cleared: %q", got)
	}
}

func TestReloadKeepsOldOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "table.json")
	writeTable(t, path, "ok:{content}")
	s, err := newWithPaths(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json at all {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err == nil {
		t.Fatal("expected reload error")
	}
	if got := render(t, s, "[x]z[/x]"); got != "ok:z" {
		t.Fatalf("old table should survive: %q", got)
	}
}

// TestTransforms exercises the optional param/content transformers directly.
func TestTransforms(t *testing.T) {
	tbl, err := loadTable([]byte(`{"tags":{
		"lc":{"content_transform":"lower","valid":"[{content}]"},
		"lp":{"param_transform":"lower","valid":"[{param}]{content}[/{param}]"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(renderBody("[lc]MiXeD[/lc]", tbl)); got != "[mixed]" {
		t.Fatalf("content transform = %q, want %q", got, "[mixed]")
	}
	if got := string(renderBody("[lp=MiXeD]body[/lp]", tbl)); got != "[mixed]body[/mixed]" {
		t.Fatalf("param transform = %q, want %q", got, "[mixed]body[/mixed]")
	}
}

// TestTableErrors rejects tables that would silently weaken safety.
func TestTableErrors(t *testing.T) {
	cases := map[string]string{
		"bad json":   `{`,
		"no tags":    `{"tags":{}}`,
		"empty name": `{"tags":{"":{"valid":"x"}}}`,
		"no valid":   `{"tags":{"x":{"param_check":"color"}}}`,
		"bad check":  `{"tags":{"x":{"valid":"{content}","param_check":"nope"}}}`,
		"bad tmpl":   `{"tags":{"x":{"valid":"{conten}"}}}`,
		"bad xform":  `{"tags":{"x":{"valid":"{content}","content_transform":"nope"}}}`,
		"void xform": `{"tags":{"x":{"valid":"x","void":true,"content_transform":"lower"}}}`,
		// noparse content is unparsed and its params are ignored, so guards,
		// transforms, and void are rejected rather than silently dead.
		"noparse check": `{"tags":{"noparse":{"valid":"{content}","content_check":"slug"}}}`,
		"noparse param": `{"tags":{"noparse":{"valid":"{content}","param_check":"color"}}}`,
		"noparse void":  `{"tags":{"noparse":{"valid":"x","void":true}}}`,
		"noparse xform": `{"tags":{"noparse":{"valid":"{content}","content_transform":"lower"}}}`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadTable([]byte(src)); err == nil {
				t.Fatalf("loadTable(%s) succeeded, want error", src)
			}
		})
	}
}

// TestDepthCap makes sure hostile nesting cannot recurse without bound.
func TestDepthCap(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	body := ""
	for i := 0; i < maxDepth+32; i++ {
		body += "[b]"
	}
	body += "x"
	// Must not panic or overflow the stack; the exact output is unimportant.
	if _, err := s.Render(body); err != nil {
		t.Fatal(err)
	}
}

// TestCloneSharesTablesSeparatesCache: a clone must observe a reload published
// by the master through the shared table pointer, while keeping its own cache
// so a body cached before the reload renders under the new table.
func TestCloneSharesTablesSeparatesCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "table.json")
	writeTable(t, path, "A:{content}")

	s, err := newWithPaths(path, "")
	if err != nil {
		t.Fatal(err)
	}
	clone := s.Clone()
	if got := render(t, clone, "[x]z[/x]"); got != "A:z" {
		t.Fatalf("clone before reload = %q", got)
	}
	// Prime both caches so a cache that is not cleared would be observable.
	_ = render(t, s, "[x]cached[/x]")
	_ = render(t, clone, "[x]cached[/x]")

	writeTable(t, path, "B:{content}")
	if err := s.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for name, r := range map[string]*Renderer{"master": s, "clone": clone} {
		if got := render(t, r, "[x]z[/x]"); got != "B:z" {
			t.Fatalf("%s after reload = %q", name, got)
		}
		if got := render(t, r, "[x]cached[/x]"); got != "B:cached" {
			t.Fatalf("%s cache not cleared after reload: %q", name, got)
		}
	}
}
