package nar

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// TestMatchesNix compares against `nix hash path` of the same tree (Nix 2.34.6).
func TestMatchesNix(t *testing.T) {
	d := t.TempDir()
	tree := filepath.Join(d, "tree")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(tree, "sub"), 0o755))
	must(os.WriteFile(filepath.Join(tree, "a.txt"), []byte("hello\n"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "sub", "run"), []byte("#!/bin/sh\necho x\n"), 0o755))
	must(os.Symlink("a.txt", filepath.Join(tree, "link")))
	must(os.WriteFile(filepath.Join(tree, "empty"), nil, 0o644))
	h := sha256.New()
	must(Dump(tree, h))
	got := "sha256-" + base64.StdEncoding.EncodeToString(h.Sum(nil))
	if want := "sha256-gGJV5pU6qzAYCy6APVs0G4g/HDTT4S41usymBUbwB3E="; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
