package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// TestStateNamespacesCoverConstants parses model.go and asserts that every
// `StateX = "..."` constant appears in the stateNamespaces table. Go has no
// reflection over const declarations, so the source is the only authoritative
// list; this guards against adding a namespace constant (and forgetting to give
// it a scope, which would silently omit it from the logout cleanup).
func TestStateNamespacesCoverConstants(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}

	nameRE := regexp.MustCompile(`^State[A-Z][A-Za-z]*$`)
	declared := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if !nameRE.MatchString(name.Name) || i >= len(vs.Values) {
				continue
			}
			lit, ok := vs.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			declared[value] = true
		}
		return true
	})

	registered := map[string]bool{}
	for _, name := range AllStateNamespaces() {
		registered[name] = true
	}

	var missing, extra []string
	for name := range declared {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	for name := range registered {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("state constants missing from stateNamespaces: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("stateNamespaces entries with no state constant: %v", extra)
	}
}

// TestSessionStateDropKeys bans a prefix that would over-match a longer
// character name, and includes every session-scoped namespace.
func TestSessionStateDropKeys(t *testing.T) {
	exact, prefixes := SessionStateDropKeys("Bob")
	for _, p := range prefixes {
		if p[len(p)-1] != '/' {
			t.Errorf("prefix %q does not end in '/'; it could over-match another character", p)
		}
	}
	wantExact := map[string]bool{
		"session/Bob": true,
		"search/Bob":  true,
		"invites/Bob": true,
		"ads/Bob":     true,
	}
	if len(exact) != len(wantExact) {
		t.Fatalf("exact keys = %v, want %v", exact, wantExact)
	}
	for _, k := range exact {
		if !wantExact[k] {
			t.Errorf("unexpected exact key %q", k)
		}
	}
	wantPrefix := map[string]bool{
		"conv/Bob/":    true,
		"summary/Bob/": true,
		"typing/Bob/":  true,
	}
	if len(prefixes) != len(wantPrefix) {
		t.Fatalf("prefixes = %v, want %v", prefixes, wantPrefix)
	}
	for _, p := range prefixes {
		if !wantPrefix[p] {
			t.Errorf("unexpected prefix %q", p)
		}
	}
}
