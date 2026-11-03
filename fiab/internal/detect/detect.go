// Package detect finds which files of a flake source tree are used by the
// store paths in a derivation closure (the derivations' inputSrcs).
//
// A source store path S matches the flake tree when S is a copy of a file
// in the tree (`./foo`), or when every file in S exists, byte for byte, at
// the same relative path below one directory D of the tree (`./dir`,
// `lib.fileset.toSource`, `cleanSource`, `src = self`, ...). The files of
// every match are reported, relative to the tree root.
package detect

import (
	"crypto/sha256"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type entry struct {
	kind   byte // 'f' file, 'x' executable, 'l' symlink
	digest string
}

func digestFile(path string, info fs.FileInfo) (entry, error) {
	if info.Mode()&fs.ModeSymlink != 0 {
		t, err := os.Readlink(path)
		if err != nil {
			return entry{}, err
		}
		return entry{'l', "link:" + t}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return entry{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return entry{}, err
	}
	k := byte('f')
	if info.Mode()&0o100 != 0 {
		k = 'x'
	}
	return entry{k, string(h.Sum(nil))}, nil
}

// walk returns all non-directory entries below root, keyed by relative path.
func walk(root string) (map[string]entry, []string, error) {
	files := map[string]entry{}
	var dirs []string
	err := filepath.Walk(root, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		e, err := digestFile(p, info)
		if err != nil {
			return err
		}
		files[rel] = e
		return nil
	})
	return files, dirs, err
}

// Tree is an indexed flake source tree.
type Tree struct {
	Root   string
	files  map[string]entry
	dirs   map[string]bool
	byHash map[string][]string
}

// Index walks a source tree.
func Index(root string) (*Tree, error) {
	files, dirs, err := walk(root)
	if err != nil {
		return nil, err
	}
	t := &Tree{Root: root, files: files, dirs: map[string]bool{}, byHash: map[string][]string{}}
	for _, d := range dirs {
		t.dirs[d] = true
	}
	for p, e := range files {
		t.byHash[e.digest] = append(t.byHash[e.digest], p)
	}
	return t, nil
}

// Match returns the tree-relative files that the store path src copies, or
// nil if src does not come from the tree.
func (t *Tree) Match(src string) ([]string, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(src)
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[i+1:]
	}
	if !info.IsDir() {
		e, err := digestFile(src, info)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range t.byHash[e.digest] {
			if filepath.Base(p) == name && t.files[p].kind == e.kind {
				out = append(out, p)
			}
		}
		return out, nil
	}
	files, _, err := walk(src)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	rels := make([]string, 0, len(files))
	for r := range files {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	first := rels[0]
	seen := map[string]bool{}
	var out []string
	for _, cand := range t.byHash[files[first].digest] {
		if cand != first && !strings.HasSuffix(cand, "/"+first) {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(cand, first), "/")
		if base == "" {
			base = "."
		}
		ok := true
		for _, r := range rels {
			tp := filepath.Join(base, r)
			if e, found := t.files[tp]; !found || e != files[r] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, r := range rels {
			tp := filepath.Join(base, r)
			if !seen[tp] {
				seen[tp] = true
				out = append(out, tp)
			}
		}
	}
	return out, nil
}
