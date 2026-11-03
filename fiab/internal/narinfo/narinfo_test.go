package narinfo

import (
	"strings"
	"testing"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/storepath"
)

// Vectors produced with `nix key generate-secret` and `nix store sign` (Nix 2.34.6).
const (
	testKey = "fiab-test-1:h+aDsCtqTJZuaQ8F0XGaXeOlppvA9UknWFUGFT1sebxERIjfrhCC9ZN2isXNRiLECnH4CwrWaoU+UowTeD2r9Q=="
	testPub = "fiab-test-1:RESI364QgvWTdorFzUYixApx+AsK1mqFPlKME3g9q/U="
	testSig = "fiab-test-1:YVKTXnFwe8BP5z72Sz+s4kWspTqkBxcfNqKsTNniqa8V4Yaz0f3hxZgGIRqocuxsMK0h2IkW7Qu3z+lfqRM5Aw=="
)

func helloNarinfo(t *testing.T, extra string) *NarInfo {
	h, err := storepath.ParseSHA256("sha256-5vDnEIkXav/FrAjGoRMKsX6wiJm/JCBTn6SF+52Gpiw=")
	if err != nil {
		t.Fatal(err)
	}
	ni, err := Parse("StorePath: /nix/store/pa6rydwgjxxmmvdxap7qk9c85h8jbimy-hello-2.12.3\n" +
		"URL: nar/x.nar.zst\nCompression: zstd\nFileHash: sha256:00\nFileSize: 1\n" +
		"NarHash: " + storepath.FormatNix32(h) + "\nNarSize: 279592\n" +
		// deliberately unsorted
		"References: pqyiidz5mi73wv5f91d48s9plcqyj5bz-glibc-2.44-57 pa6rydwgjxxmmvdxap7qk9c85h8jbimy-hello-2.12.3\n" + extra)
	if err != nil {
		t.Fatal(err)
	}
	return ni
}

func TestSignMatchesNix(t *testing.T) {
	k, err := ParseSecretKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if k.PublicKey() != testPub {
		t.Fatalf("public key %s, want %s", k.PublicKey(), testPub)
	}
	signed, err := SignIfNeeded(helloNarinfo(t, ""), k)
	if err != nil {
		t.Fatal(err)
	}
	if got := signed.Get("Sig"); got != testSig {
		t.Fatalf("signature\n got %s\nwant %s", got, testSig)
	}
	fp, _ := helloNarinfo(t, "").Fingerprint()
	if !strings.HasPrefix(fp, "1;/nix/store/pa6rydwgjxxmmvdxap7qk9c85h8jbimy-hello-2.12.3;sha256:") ||
		!strings.HasSuffix(fp, ";279592;/nix/store/pa6rydwgjxxmmvdxap7qk9c85h8jbimy-hello-2.12.3,/nix/store/pqyiidz5mi73wv5f91d48s9plcqyj5bz-glibc-2.44-57") {
		t.Fatalf("fingerprint %q", fp)
	}
}

func TestCANotSigned(t *testing.T) {
	k, _ := ParseSecretKey(testKey)
	ni := helloNarinfo(t, "CA: fixed:r:sha256:1rlklj7jgxf96zmz5z0ha9qw1mdal3vhwsdd40pmv0b75xgc7lz1\n")
	out, err := SignIfNeeded(ni, k)
	if err != nil {
		t.Fatal(err)
	}
	if out.Has("Sig") {
		t.Fatal("CA narinfo was signed")
	}
}

func TestNoDoubleSign(t *testing.T) {
	k, _ := ParseSecretKey(testKey)
	once, _ := SignIfNeeded(helloNarinfo(t, ""), k)
	twice, _ := SignIfNeeded(once, k)
	if strings.Count(twice.String(), "Sig: ") != 1 {
		t.Fatal("signed twice")
	}
}

func TestBadKeys(t *testing.T) {
	for _, s := range []string{"", "nocolon", "name:!!!", "name:AAAA"} {
		if _, err := ParseSecretKey(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
