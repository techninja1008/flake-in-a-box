// Package rewrite implements streaming string rewriting with the same
// semantics as Nix's RewritingSink (libstore/references.cc), and the
// content-addressing rewrite performed by `nix store make-content-addressed`.
package rewrite

import (
	"bytes"
	"io"
	"sort"
)

// Writer rewrites occurrences of equal-length from→to pairs in a byte
// stream, holding back just enough bytes to catch matches spanning writes.
// Like Nix, rewrites are applied one key at a time in sorted key order over
// the buffered data, and match positions are recorded (as stream offsets of
// the data passed downstream) for every replacement.
type Writer struct {
	keys    []string
	rewrite map[string]string
	maxLen  int
	next    io.Writer
	prev    []byte
	pos     uint64
	// Matches holds the stream offsets at which replacements were made.
	Matches map[uint64]struct{}
	record  bool
}

// New returns a rewriting writer. Keys and values must have equal lengths.
func New(rewrites map[string]string, next io.Writer, recordMatches bool) *Writer {
	w := &Writer{rewrite: rewrites, next: next, record: recordMatches}
	for k, v := range rewrites {
		if len(k) != len(v) {
			panic("rewrite: from and to must have equal length")
		}
		if k == v {
			continue
		}
		w.keys = append(w.keys, k)
		if len(k) > w.maxLen {
			w.maxLen = len(k)
		}
	}
	sort.Strings(w.keys)
	if recordMatches {
		w.Matches = map[uint64]struct{}{}
	}
	return w
}

func (w *Writer) Write(data []byte) (int, error) {
	s := make([]byte, 0, len(w.prev)+len(data))
	s = append(s, w.prev...)
	s = append(s, data...)
	for _, k := range w.keys {
		kb := []byte(k)
		vb := []byte(w.rewrite[k])
		j := 0
		for {
			i := bytes.Index(s[j:], kb)
			if i < 0 {
				break
			}
			j += i
			if w.record {
				w.Matches[uint64(j)+w.pos] = struct{}{}
			}
			copy(s[j:], vb)
			// Nix resumes the search at the start of the replacement.
		}
	}
	var keep int
	switch {
	case w.maxLen == 0:
		keep = 0
	case len(s) < w.maxLen:
		keep = len(s)
	default:
		keep = w.maxLen - 1
	}
	consumed := len(s) - keep
	w.prev = append(w.prev[:0], s[consumed:]...)
	w.pos += uint64(consumed)
	if consumed > 0 {
		if _, err := w.next.Write(s[:consumed]); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

// Flush writes any held-back bytes.
func (w *Writer) Flush() error {
	if len(w.prev) == 0 {
		return nil
	}
	w.pos += uint64(len(w.prev))
	_, err := w.next.Write(w.prev)
	w.prev = w.prev[:0]
	return err
}

// SortedMatches returns the recorded match positions in ascending order.
func (w *Writer) SortedMatches() []uint64 {
	out := make([]uint64, 0, len(w.Matches))
	for p := range w.Matches {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
