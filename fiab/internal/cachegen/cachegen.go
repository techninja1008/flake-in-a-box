// Package cachegen writes binary caches: CA rewrites of closures (the
// equivalent of `nix store make-content-addressed`) and packed copies of
// existing flat-file caches.
package cachegen

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/nar"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/narinfo"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/nix32"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/rewrite"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/storepath"
)

// PackedNarinfos is the file holding all narinfos of a packed cache, as a
// gzip-compressed JSON object mapping hash part to narinfo text. Packing
// keeps store hashes out of the cache's own store path, so the cache does
// not retain (reference) the paths it contains.
const PackedNarinfos = "narinfos.json.gz"

// ClosureItem is one entry of an exportReferencesGraph closure.
type ClosureItem struct {
	Path       string   `json:"path"`
	NarHash    string   `json:"narHash"`
	NarSize    uint64   `json:"narSize"`
	References []string `json:"references"`
	CA         string   `json:"ca,omitempty"`
}

// LoadClosure reads closures from a structured-attrs JSON file, merging the
// given attribute names (or all list-valued attributes holding closures).
func LoadClosure(attrsFile string, attrs []string) ([]ClosureItem, error) {
	raw, err := os.ReadFile(attrsFile)
	if err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ClosureItem
	for _, a := range attrs {
		v, ok := all[a]
		if !ok {
			return nil, fmt.Errorf("attribute %q not in %s", a, attrsFile)
		}
		var items []ClosureItem
		if err := json.Unmarshal(v, &items); err != nil {
			return nil, fmt.Errorf("attribute %q: %w", a, err)
		}
		for _, it := range items {
			if !seen[it.Path] {
				seen[it.Path] = true
				out = append(out, it)
			}
		}
	}
	return out, nil
}

// Compressor describes NAR compression.
type Compressor struct {
	Name string // as written in narinfo: none, zstd, xz
	Ext  string
	Cmd  []string
}

// GetCompressor returns a compressor by name.
func GetCompressor(name string) (Compressor, error) {
	switch name {
	case "none":
		return Compressor{"none", "", nil}, nil
	case "zstd":
		return Compressor{"zstd", ".zst", []string{"zstd", "-q", "-c", "-T1", "-19"}}, nil
	case "xz":
		return Compressor{"xz", ".xz", []string{"xz", "-c", "-T1"}}, nil
	}
	return Compressor{}, fmt.Errorf("unknown compression %q", name)
}

type counter struct{ n uint64 }

func (c *counter) Write(p []byte) (int, error) { c.n += uint64(len(p)); return len(p), nil }

// writeNar streams a NAR through fn into a compressed file under dir/nar,
// returning narinfo URL/FileHash/FileSize fields.
func writeNar(dir string, comp Compressor, produce func(w io.Writer) error) (url, fileHash string, fileSize uint64, err error) {
	tmp, err := os.CreateTemp(filepath.Join(dir, "nar"), "tmp-")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	fh := sha256.New()
	cnt := &counter{}
	sink := io.MultiWriter(tmp, fh, cnt)
	if comp.Cmd == nil {
		err = produce(sink)
	} else {
		cmd := exec.Command(comp.Cmd[0], comp.Cmd[1:]...)
		cmd.Stdout = sink
		cmd.Stderr = os.Stderr
		var stdin io.WriteCloser
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return
		}
		if err = cmd.Start(); err != nil {
			return
		}
		perr := produce(stdin)
		stdin.Close()
		werr := cmd.Wait()
		if perr != nil {
			err = perr
		} else {
			err = werr
		}
	}
	if err != nil {
		tmp.Close()
		return
	}
	if err = tmp.Close(); err != nil {
		return
	}
	h := nix32.Encode(fh.Sum(nil))
	url = "nar/" + h + ".nar" + comp.Ext
	if err = os.Rename(tmp.Name(), filepath.Join(dir, url)); err != nil {
		return
	}
	return url, "sha256:" + h, cnt.n, nil
}

// TopoSort orders items so that every path comes after its references.
func TopoSort(items []ClosureItem) ([]ClosureItem, error) {
	byPath := map[string]ClosureItem{}
	for _, it := range items {
		byPath[it.Path] = it
	}
	state := map[string]int{}
	var out []ClosureItem
	var visit func(p string) error
	visit = func(p string) error {
		switch state[p] {
		case 1:
			return fmt.Errorf("reference cycle through %s", p)
		case 2:
			return nil
		}
		state[p] = 1
		it, ok := byPath[p]
		if !ok {
			return fmt.Errorf("closure is missing %s", p)
		}
		refs := append([]string(nil), it.References...)
		sort.Strings(refs)
		for _, r := range refs {
			if r != p {
				if err := visit(r); err != nil {
					return err
				}
			}
		}
		state[p] = 2
		out = append(out, it)
		return nil
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := visit(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Variant selects how self-references are hashed. Nix 2.17-2.34 hash a
// self-referencing NAR with the self-references zeroed out only; Nix 2.35
// restored hashing their positions as well (NixOS/nix#15931). The two give
// different store paths, and a client only accepts its own variant.
type Variant string

const (
	// VariantPositions matches Nix >= 2.35.
	VariantPositions Variant = "positions"
	// VariantLegacy matches Nix 2.17 - 2.34.
	VariantLegacy Variant = "legacy"
)

// ParseVariant validates a variant name.
func ParseVariant(s string) (Variant, error) {
	switch Variant(s) {
	case VariantPositions, VariantLegacy:
		return Variant(s), nil
	}
	return "", fmt.Errorf("unknown self-reference hashing variant %q (want positions or legacy)", s)
}

// RewriteResult is the outcome of rewriting one path.
type RewriteResult struct {
	Old, New string
	NarInfo  *narinfo.NarInfo
}

// RewriteOne computes the content-addressed version of item, given the
// remappings of its references, writes its NAR into dir and returns its
// narinfo. It mirrors nix::makeContentAddressed exactly.
func RewriteOne(dir string, comp Compressor, it ClosureItem, remap map[string]string, root string, variant Variant) (*RewriteResult, error) {
	oldHash := storepath.HashPart(it.Path)
	refRewrites := map[string]string{}
	var newRefs []string
	self := false
	for _, r := range it.References {
		if r == it.Path {
			self = true
			continue
		}
		nr, ok := remap[r]
		if !ok {
			return nil, fmt.Errorf("%s: reference %s not yet rewritten", it.Path, r)
		}
		if nr != r {
			refRewrites[r] = nr
		}
		newRefs = append(newRefs, nr)
	}
	src := root + it.Path

	expected, err := storepath.ParseSHA256(it.NarHash)
	if err != nil && it.NarHash != "" {
		return nil, err
	}

	// Pass 1: hash the reference-rewritten NAR with self-references masked.
	masked := sha256.New()
	maskW := rewrite.New(map[string]string{oldHash: strings.Repeat("\x00", len(oldHash))}, masked, true)
	refW := rewrite.New(refRewrites, maskW, false)
	orig := sha256.New()
	cnt := &counter{}
	if err := nar.Dump(src, io.MultiWriter(orig, cnt, refW)); err != nil {
		return nil, err
	}
	if err := refW.Flush(); err != nil {
		return nil, err
	}
	if err := maskW.Flush(); err != nil {
		return nil, err
	}
	if expected != nil && string(orig.Sum(nil)) != string(expected) {
		return nil, fmt.Errorf("%s: NAR hash mismatch (serialiser bug or modified store path)", it.Path)
	}
	if it.NarSize != 0 && cnt.n != it.NarSize {
		return nil, fmt.Errorf("%s: NAR size %d, expected %d", it.Path, cnt.n, it.NarSize)
	}
	if variant == VariantPositions {
		for _, p := range maskW.SortedMatches() {
			io.WriteString(masked, "|"+strconv.FormatUint(p, 10))
		}
	}
	caHash := masked.Sum(nil)
	newPath := storepath.MakeSourcePath(storepath.Name(it.Path), caHash, newRefs, self)
	newHash := storepath.HashPart(newPath)

	// Pass 2: produce the final NAR.
	var narHash hash.Hash = sha256.New()
	narCnt := &counter{}
	url, fileHash, fileSize, err := writeNar(dir, comp, func(w io.Writer) error {
		selfW := rewrite.New(map[string]string{oldHash: newHash}, io.MultiWriter(w, narHash, narCnt), false)
		refW := rewrite.New(refRewrites, selfW, false)
		if err := nar.Dump(src, refW); err != nil {
			return err
		}
		if err := refW.Flush(); err != nil {
			return err
		}
		return selfW.Flush()
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", it.Path, err)
	}

	refNames := []string{}
	for _, r := range newRefs {
		refNames = append(refNames, storepath.BaseName(r))
	}
	if self {
		refNames = append(refNames, storepath.BaseName(newPath))
	}
	sort.Strings(refNames)
	ni := &narinfo.NarInfo{Fields: []narinfo.Field{
		{"StorePath", newPath},
		{"URL", url},
		{"Compression", comp.Name},
		{"FileHash", fileHash},
		{"FileSize", strconv.FormatUint(fileSize, 10)},
		{"NarHash", storepath.FormatNix32(narHash.Sum(nil))},
		{"NarSize", strconv.FormatUint(narCnt.n, 10)},
		{"References", strings.Join(refNames, " ")},
		{"CA", "fixed:r:" + storepath.FormatNix32(caHash)},
	}}
	return &RewriteResult{Old: it.Path, New: newPath, NarInfo: ni}, nil
}

// RewriteClosure rewrites a whole closure into dir (creating dir/nar),
// using up to jobs parallel workers. It returns the old→new mapping and
// the narinfos keyed by new hash part.
func RewriteClosure(items []ClosureItem, dir string, comp Compressor, jobs int, root string, variant Variant) (map[string]string, map[string]string, error) {
	sorted, err := TopoSort(items)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "nar"), 0o755); err != nil {
		return nil, nil, err
	}
	if jobs < 1 {
		jobs = 1
	}
	var mu sync.Mutex
	remap := map[string]string{}
	infos := map[string]string{}
	done := map[string]chan struct{}{}
	for _, it := range sorted {
		done[it.Path] = make(chan struct{})
	}
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	var firstErr error
	failed := make(chan struct{})
	var failOnce sync.Once
	for _, it := range sorted {
		it := it
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done[it.Path])
			for _, r := range it.References {
				if r == it.Path {
					continue
				}
				select {
				case <-done[r]:
				case <-failed:
					return
				}
			}
			select {
			case sem <- struct{}{}:
			case <-failed:
				return
			}
			defer func() { <-sem }()
			mu.Lock()
			local := make(map[string]string, len(it.References))
			for _, r := range it.References {
				if v, ok := remap[r]; ok {
					local[r] = v
				}
			}
			mu.Unlock()
			res, err := RewriteOne(dir, comp, it, local, root, variant)
			if err != nil {
				failOnce.Do(func() { firstErr = err; close(failed) })
				return
			}
			mu.Lock()
			remap[res.Old] = res.New
			infos[storepath.HashPart(res.New)] = res.NarInfo.String()
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "rewrote %s -> %s\n", res.Old, res.New)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, firstErr
	}
	return remap, infos, nil
}

// WriteCacheInfo writes nix-cache-info.
func WriteCacheInfo(dir string, priority int) error {
	s := "StoreDir: " + storepath.StoreDir + "\n"
	if priority > 0 {
		s += "Priority: " + strconv.Itoa(priority) + "\n"
	}
	return os.WriteFile(filepath.Join(dir, "nix-cache-info"), []byte(s), 0o644)
}

// WritePacked writes the packed narinfo file.
func WritePacked(dir string, infos map[string]string) error {
	f, err := os.Create(filepath.Join(dir, PackedNarinfos))
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(infos); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadPacked reads a packed narinfo file.
func ReadPacked(dir string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(dir, PackedNarinfos))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.NewDecoder(zr).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// ReadFlat reads the *.narinfo files of a flat-file cache.
func ReadFlat(dir string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".narinfo") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		m[strings.TrimSuffix(name, ".narinfo")] = string(b)
	}
	return m, nil
}

// ReadAny reads narinfos from a packed or flat cache directory.
func ReadAny(dir string) (map[string]string, error) {
	if _, err := os.Stat(filepath.Join(dir, PackedNarinfos)); err == nil {
		return ReadPacked(dir)
	}
	return ReadFlat(dir)
}

// Pack copies the NARs of flat or packed caches into out and writes a single
// packed narinfo file. NAR files are copied, never linked, so out does not
// reference the inputs.
func Pack(out string, inputs []string) error {
	if err := os.MkdirAll(filepath.Join(out, "nar"), 0o755); err != nil {
		return err
	}
	all := map[string]string{}
	for _, in := range inputs {
		infos, err := ReadAny(in)
		if err != nil {
			return fmt.Errorf("%s: %w", in, err)
		}
		for h, text := range infos {
			ni, err := narinfo.Parse(text)
			if err != nil {
				return fmt.Errorf("%s/%s.narinfo: %w", in, h, err)
			}
			url := ni.Get("URL")
			if url == "" || strings.Contains(url, "..") || strings.HasPrefix(url, "/") {
				return fmt.Errorf("%s/%s.narinfo: bad URL %q", in, h, url)
			}
			dst := filepath.Join(out, url)
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				if err := copyFile(filepath.Join(in, url), dst); err != nil {
					return err
				}
			}
			all[h] = text
		}
	}
	if err := WriteCacheInfo(out, 0); err != nil {
		return err
	}
	return WritePacked(out, all)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
