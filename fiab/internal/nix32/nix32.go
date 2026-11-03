// Package nix32 implements Nix's base-32 encoding ("nix32").
package nix32

import "fmt"

// Alphabet omits e, o, u and t.
const Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

var reverse [256]int8

func init() {
	for i := range reverse {
		reverse[i] = -1
	}
	for i := 0; i < len(Alphabet); i++ {
		reverse[Alphabet[i]] = int8(i)
	}
}

// EncodedLen returns the length of the nix32 encoding of n bytes.
func EncodedLen(n int) int {
	if n == 0 {
		return 0
	}
	return (n*8-1)/5 + 1
}

// Encode encodes b exactly like BaseNix32::encode in Nix.
func Encode(b []byte) string {
	l := EncodedLen(len(b))
	out := make([]byte, 0, l)
	for n := l - 1; n >= 0; n-- {
		bit := uint(n * 5)
		i := bit / 8
		j := bit % 8
		c := b[i] >> j
		if int(i) < len(b)-1 {
			c |= b[i+1] << (8 - j)
		}
		out = append(out, Alphabet[c&0x1f])
	}
	return string(out)
}

// Decode decodes a nix32 string into a byte slice of the implied size.
func Decode(s string) ([]byte, error) {
	size := len(s) * 5 / 8
	out := make([]byte, size)
	for n := 0; n < len(s); n++ {
		c := s[len(s)-n-1]
		d := reverse[c]
		if d < 0 {
			return nil, fmt.Errorf("invalid nix32 character %q", c)
		}
		bit := uint(n * 5)
		i := bit / 8
		j := bit % 8
		out[i] |= byte(d) << j
		if int(i) < size-1 {
			out[i+1] |= byte(d) >> (8 - j)
		} else if byte(d)>>(8-j) != 0 {
			return nil, fmt.Errorf("invalid nix32 string %q", s)
		}
	}
	return out, nil
}

// IsChar reports whether c is in the nix32 alphabet.
func IsChar(c byte) bool { return reverse[c] >= 0 }
