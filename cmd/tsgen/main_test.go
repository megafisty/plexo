package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedTypesIsCurrent and TestGeneratedEnumsIsCurrent fail when the
// committed generated file differs from a fresh generation, so a Go boundary
// change cannot silently leave the client types stale. Run
// `go generate ./cmd/tsgen` to refresh both.
func TestGeneratedTypesIsCurrent(t *testing.T) {
	checkGenerated(t, filepath.Join("..", "..", "ui", "src", "transport", "types.gen.ts"), Generate)
}

func TestGeneratedEnumsIsCurrent(t *testing.T) {
	checkGenerated(t, filepath.Join("..", "..", "ui", "src", "transport", "enums.ts"), GenerateEnums)
}

func checkGenerated(t *testing.T, path string, gen func() ([]byte, error)) {
	t.Helper()
	want, err := gen()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run `go generate ./cmd/tsgen`", path)
	}
}
