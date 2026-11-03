package nix32

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, n := range []int{1, 2, 5, 20, 32, 64} {
		b := make([]byte, n)
		rand.Read(b)
		s := Encode(b)
		if len(s) != EncodedLen(n) {
			t.Fatalf("len %d", len(s))
		}
		d, err := Decode(s)
		if err != nil || !bytes.Equal(d, b) {
			t.Fatalf("roundtrip %d: %v", n, err)
		}
	}
	if _, err := Decode("eeee"); err == nil {
		t.Fatal("accepted invalid characters")
	}
}
