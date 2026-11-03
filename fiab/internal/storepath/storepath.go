// Package storepath computes Nix store paths and handles hash formats.
package storepath

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/nix32"
)

// HashLen is the length of the hash part of a store path.
const HashLen = 32

// StoreDir is the store directory, overridable for tests.
var StoreDir = "/nix/store"

// HashPart returns the 32-character hash part of a store path or base name.
func HashPart(p string) string {
	b := BaseName(p)
	if len(b) < HashLen {
		return b
	}
	return b[:HashLen]
}

// BaseName strips the store directory.
func BaseName(p string) string {
	return strings.TrimPrefix(p, StoreDir+"/")
}

// Name returns the name part (after "<hash>-").
func Name(p string) string {
	b := BaseName(p)
	if len(b) <= HashLen+1 {
		return ""
	}
	return b[HashLen+1:]
}

func compress(h []byte, n int) []byte {
	out := make([]byte, n)
	for i, b := range h {
		out[i%n] ^= b
	}
	return out
}

// Make implements StoreDirConfig::makeStorePath with a SHA-256 inner hash.
func Make(typ string, innerSHA256 []byte, name string) string {
	s := typ + ":sha256:" + hex.EncodeToString(innerSHA256) + ":" + StoreDir + ":" + name
	sum := sha256.Sum256([]byte(s))
	return StoreDir + "/" + nix32.Encode(compress(sum[:], 20)) + "-" + name
}

// MakeSourcePath computes the path of a fixed:r:sha256 (NAR, SHA-256)
// content-addressed object with the given (full-path) references.
func MakeSourcePath(name string, narSHA256 []byte, refs []string, self bool) string {
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	typ := "source"
	for _, r := range sorted {
		typ += ":" + r
	}
	if self {
		typ += ":self"
	}
	return Make(typ, narSHA256, name)
}

// ParseSHA256 parses "sha256:<nix32|hex|base64>" or SRI "sha256-<base64>".
func ParseSHA256(s string) ([]byte, error) {
	var rest string
	switch {
	case strings.HasPrefix(s, "sha256:"):
		rest = s[len("sha256:"):]
	case strings.HasPrefix(s, "sha256-"):
		b, err := base64.StdEncoding.DecodeString(s[len("sha256-"):])
		if err != nil {
			return nil, err
		}
		return checkLen(b, s)
	default:
		rest = s
	}
	switch len(rest) {
	case 52:
		b, err := nix32.Decode(rest)
		if err != nil {
			return nil, err
		}
		return checkLen(b, s)
	case 64:
		b, err := hex.DecodeString(rest)
		if err != nil {
			return nil, err
		}
		return b, nil
	case 44:
		b, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return nil, err
		}
		return checkLen(b, s)
	}
	return nil, fmt.Errorf("unrecognised sha256 hash %q", s)
}

func checkLen(b []byte, s string) ([]byte, error) {
	if len(b) != 32 {
		return nil, fmt.Errorf("hash %q has wrong length", s)
	}
	return b, nil
}

// FormatNix32 renders "sha256:<nix32>".
func FormatNix32(h []byte) string { return "sha256:" + nix32.Encode(h) }

// FormatSRI renders "sha256-<base64>".
func FormatSRI(h []byte) string { return "sha256-" + base64.StdEncoding.EncodeToString(h) }
