package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func write(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestMatch(t *testing.T) {
	tree := t.TempDir()
	write(t, filepath.Join(tree, "flake.nix"), "{ }", 0o644)
	write(t, filepath.Join(tree, "src/a.sh"), "echo a", 0o755)
	write(t, filepath.Join(tree, "src/data.txt"), "data", 0o644)
	write(t, filepath.Join(tree, "docs/notes.md"), "notes", 0o644)
	write(t, filepath.Join(tree, "patches/fix.patch"), "patch", 0o644)
	idx, err := Index(tree)
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	// lib.fileset.toSource { root = ./.; fileset = ./src; }
	write(t, filepath.Join(store, "h1-source/src/a.sh"), "echo a", 0o755)
	write(t, filepath.Join(store, "h1-source/src/data.txt"), "data", 0o644)
	// ./patches/fix.patch coerced to a store path
	write(t, filepath.Join(store, "h2-fix.patch"), "patch", 0o444)
	// a nixpkgs patch that happens to share content but not name
	write(t, filepath.Join(store, "h3-other.patch"), "patch", 0o444)
	// ./src as a directory copy
	write(t, filepath.Join(store, "h4-src/a.sh"), "echo a", 0o755)
	write(t, filepath.Join(store, "h4-src/data.txt"), "data", 0o644)
	// differing executable bit: no match
	write(t, filepath.Join(store, "h5-source/src/a.sh"), "echo a", 0o644)

	cases := map[string][]string{
		"h1-source":      {"src/a.sh", "src/data.txt"},
		"h2-fix.patch":   {"patches/fix.patch"},
		"h3-other.patch": nil,
		"h4-src":         {"src/a.sh", "src/data.txt"},
		"h5-source":      nil,
	}
	for name, want := range cases {
		got, err := idx.Match(filepath.Join(store, name))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}
