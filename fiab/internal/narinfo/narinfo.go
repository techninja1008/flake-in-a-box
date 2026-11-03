// Package narinfo parses, renders and signs .narinfo files.
package narinfo

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/storepath"
)

// NarInfo is a parsed narinfo. Unknown fields are kept in order.
type NarInfo struct {
	Fields []Field
}

// Field is one "Key: value" line.
type Field struct{ Key, Value string }

// Parse parses narinfo text.
func Parse(text string) (*NarInfo, error) {
	ni := &NarInfo{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			return nil, fmt.Errorf("malformed narinfo line %q", line)
		}
		ni.Fields = append(ni.Fields, Field{k, v})
	}
	if ni.Get("StorePath") == "" {
		return nil, fmt.Errorf("narinfo lacks StorePath")
	}
	return ni, nil
}

// Get returns the first value for key.
func (n *NarInfo) Get(key string) string {
	for _, f := range n.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// Has reports whether key is present.
func (n *NarInfo) Has(key string) bool {
	for _, f := range n.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// String renders the narinfo.
func (n *NarInfo) String() string {
	var b strings.Builder
	for _, f := range n.Fields {
		b.WriteString(f.Key)
		b.WriteString(": ")
		b.WriteString(f.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// Fingerprint returns the string Nix signs: 1;<path>;<narHash>;<narSize>;<refs>.
func (n *NarInfo) Fingerprint() (string, error) {
	sp := n.Get("StorePath")
	nh := n.Get("NarHash")
	ns := n.Get("NarSize")
	if !strings.HasPrefix(nh, "sha256:") {
		return "", fmt.Errorf("%s: NarHash %q must be sha256:<nix32>", sp, nh)
	}
	if ns == "" {
		return "", fmt.Errorf("%s: missing NarSize", sp)
	}
	dir := sp[:strings.LastIndex(sp, "/")]
	var refs []string
	for _, r := range strings.Fields(n.Get("References")) {
		refs = append(refs, dir+"/"+r)
	}
	sort.Strings(refs)
	return "1;" + sp + ";" + nh + ";" + ns + ";" + strings.Join(refs, ","), nil
}

// SecretKey is a Nix ed25519 signing key.
type SecretKey struct {
	Name string
	Key  ed25519.PrivateKey
}

// ParseSecretKey parses "name:base64(64 bytes)" (or a 32-byte seed).
func ParseSecretKey(s string) (*SecretKey, error) {
	name, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || name == "" {
		return nil, fmt.Errorf("secret key must have the form <name>:<base64>")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize, ed25519.SeedSize:
	default:
		return nil, fmt.Errorf("secret key has %d bytes, want 64", len(raw))
	}
	return &SecretKey{Name: name, Key: ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])}, nil
}

// LoadSecretKey reads a key file.
func LoadSecretKey(path string) (*SecretKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseSecretKey(string(b))
}

// PublicKey renders "name:base64(pub)".
func (k *SecretKey) PublicKey() string {
	return k.Name + ":" + base64.StdEncoding.EncodeToString(k.Key.Public().(ed25519.PublicKey))
}

// Sign returns "name:base64(sig)" over msg.
func (k *SecretKey) Sign(msg string) string {
	return k.Name + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(k.Key, []byte(msg)))
}

// IsContentAddressed reports whether the narinfo carries a CA field.
func (n *NarInfo) IsContentAddressed() bool { return n.Get("CA") != "" }

// SignIfNeeded appends a Sig line for input-addressed narinfos. CA
// narinfos (and those already signed by this key) are returned unchanged.
func SignIfNeeded(n *NarInfo, k *SecretKey) (*NarInfo, error) {
	if k == nil || n.IsContentAddressed() {
		return n, nil
	}
	for _, f := range n.Fields {
		if f.Key == "Sig" && strings.HasPrefix(f.Value, k.Name+":") {
			return n, nil
		}
	}
	fp, err := n.Fingerprint()
	if err != nil {
		return nil, err
	}
	out := &NarInfo{Fields: append(append([]Field(nil), n.Fields...), Field{"Sig", k.Sign(fp)})}
	return out, nil
}

var _ = storepath.HashLen
