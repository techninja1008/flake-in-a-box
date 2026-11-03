package cachegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/narinfo"
)

const (
	dep     = "/nix/store/f57bpgshzf8kc8ppkchk0y9xk6ycvz7x-fiab-dep"
	selfref = "/nix/store/277k3x0cr7wjnm5w84038ixf2bshcxi1-fiab-selfref"
)

// fixture recreates a two-path closure built by Nix: fiab-dep (writeText)
// and fiab-selfref, which refers to itself (several times) and to fiab-dep.
func fixture(t *testing.T) (string, []ClosureItem) {
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	d := filepath.Join(root, dep)
	must(os.MkdirAll(filepath.Dir(d), 0o755))
	must(os.WriteFile(d, []byte("dependency\n"), 0o444))
	s := filepath.Join(root, selfref)
	must(os.MkdirAll(s, 0o755))
	must(os.WriteFile(filepath.Join(s, "copy"), []byte("dependency\n"), 0o444))
	must(os.Symlink(dep, filepath.Join(s, "dep")))
	must(os.WriteFile(filepath.Join(s, "ref"), []byte(dep+"\n"), 0o444))
	must(os.WriteFile(filepath.Join(s, "self"), []byte(selfref+"/self\n"), 0o444))
	must(os.WriteFile(filepath.Join(s, "twice"), []byte(selfref+" "+selfref+"\n"), 0o444))
	return root, []ClosureItem{
		{Path: selfref, NarHash: "sha256-EPnkumdaFEZdGU9jw4HMwcwUNiBtt38zxSM3KHoV01I=", NarSize: 1328, References: []string{selfref, dep}},
		{Path: dep, NarHash: "sha256-hM9qMQH0zuQEUE29tOkmNLYj4ZX49+gCdh67uzlPgY8=", NarSize: 128},
	}
}

// Expected results of `nix store make-content-addressed` on that closure.
func TestRewriteMatchesNix(t *testing.T) {
	for variant, want := range map[Variant]string{
		VariantLegacy:    "/nix/store/pfwy97fdgi6lkyan1rfby4lrms666nbv-fiab-selfref", // Nix 2.34.6
		VariantPositions: "/nix/store/4nbhdwa2kg2nflg8m1xqv6vayb3fwkgs-fiab-selfref", // Nix 2.35.3
	} {
		root, items := fixture(t)
		out := t.TempDir()
		comp, _ := GetCompressor("none")
		remap, infos, err := RewriteClosure(items, out, comp, 2, root, variant)
		if err != nil {
			t.Fatal(err)
		}
		if remap[selfref] != want {
			t.Errorf("%s: got %s want %s", variant, remap[selfref], want)
		}
		// The rewritten NAR must contain neither old path, and the narinfo
		// must carry a CA field and list itself as a reference.
		text := infos[strings.TrimPrefix(want, "/nix/store/")[:32]]
		ni, err := narinfo.Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ni.Get("CA"), "fixed:r:sha256:") {
			t.Errorf("CA field %q", ni.Get("CA"))
		}
		if !strings.Contains(ni.Get("References"), strings.TrimPrefix(want, "/nix/store/")) {
			t.Errorf("self reference missing: %q", ni.Get("References"))
		}
		nar, err := os.ReadFile(filepath.Join(out, ni.Get("URL")))
		if err != nil {
			t.Fatal(err)
		}
		for _, old := range []string{"277k3x0cr7wjnm5w84038ixf2bshcxi1", "f57bpgshzf8kc8ppkchk0y9xk6ycvz7x"} {
			if strings.Contains(string(nar), old) {
				t.Errorf("%s: rewritten NAR still contains %s", variant, old)
			}
		}
	}
}

func TestNarHashMismatchDetected(t *testing.T) {
	root, items := fixture(t)
	items[1].NarHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	comp, _ := GetCompressor("none")
	if _, _, err := RewriteClosure(items, t.TempDir(), comp, 1, root, VariantPositions); err == nil {
		t.Fatal("expected a NAR hash mismatch")
	}
}

func TestPackRoundTrip(t *testing.T) {
	root, items := fixture(t)
	ca := t.TempDir()
	comp, _ := GetCompressor("none")
	_, infos, err := RewriteClosure(items, ca, comp, 1, root, VariantLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePacked(ca, infos); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "packed")
	if err := Pack(out, []string{ca}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPacked(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("packed %d narinfos", len(got))
	}
	for _, text := range got {
		ni, _ := narinfo.Parse(text)
		if _, err := os.Stat(filepath.Join(out, ni.Get("URL"))); err != nil {
			t.Fatal(err)
		}
	}
}
