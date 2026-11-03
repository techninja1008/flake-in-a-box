package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/cachegen"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/narinfo"
)

const (
	testKey = "fiab-test-1:h+aDsCtqTJZuaQ8F0XGaXeOlppvA9UknWFUGFT1sebxERIjfrhCC9ZN2isXNRiLECnH4CwrWaoU+UowTeD2r9Q=="
	iaHash  = "pa6rydwgjxxmmvdxap7qk9c85h8jbimy"
	caHash  = "i19krds6qaq5cmz75mbcmjilnyyxg5ms"
)

func testBundle(t *testing.T) string {
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "dist"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "dist", "ca-rewrite-x.tar.gz"), []byte("ca dist"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "dist", "filtered-y.tar.gz"), []byte("filtered dist"), 0o644))
	cache := filepath.Join(dir, "cache", "main")
	must(os.MkdirAll(filepath.Join(cache, "nar"), 0o755))
	must(os.WriteFile(filepath.Join(cache, "nar", "abc.nar"), []byte("nar bytes"), 0o644))
	must(cachegen.WritePacked(cache, map[string]string{
		iaHash: "StorePath: /nix/store/" + iaHash + "-hello-2.12.3\nURL: nar/abc.nar\nCompression: none\n" +
			"NarHash: sha256:0dji0w21i7v0b468ckd6v9phpz8fyr02slqcz5sw1dvmavlvwpir\nNarSize: 279592\n" +
			"References: " + iaHash + "-hello-2.12.3 pqyiidz5mi73wv5f91d48s9plcqyj5bz-glibc-2.44-57\n",
		caHash: "StorePath: /nix/store/" + caHash + "-libunistring-1.4.2\nURL: nar/abc.nar\nCompression: none\n" +
			"NarHash: sha256:0c1v913qa4lmdmy7kc0qlhb8adbb9jh3d7hdbdzmajqlc69zrhpc\nNarSize: 2083096\n" +
			"References: " + caHash + "-libunistring-1.4.2\nCA: fixed:r:sha256:1rlklj7jgxf96zmz5z0ha9qw1mdal3vhwsdd40pmv0b75xgc7lz1\n",
	}))
	lock := `{"nodes":{"nixpkgs":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs","rev":"0bb2eedc20e4739f29e8045034f2828ad7054f8f","narHash":"sha256-x"},"original":{"type":"github","owner":"NixOS","repo":"nixpkgs"}},` +
		`"utils":{"inputs":{"nixpkgs":["nixpkgs"]},"locked":{"type":"github","owner":"o","repo":"u","rev":"1","narHash":"sha256-y"},"original":{"type":"github","owner":"o","repo":"u"}},` +
		`"root":{"inputs":{"nixpkgs":"nixpkgs","utils":"utils"}}},"root":"root","version":7}`
	b := map[string]any{
		"version": 1, "name": "test", "description": `has "quotes" and ${interp}`, "defaultMode": "ca-rewrite",
		"caches": []string{"cache/main"},
		"modes": []any{
			map[string]any{"name": "ca-rewrite", "kind": "lib", "dist": "dist/ca-rewrite-x.tar.gz", "narHash": "sha256-AAA=", "lastModified": 1},
			map[string]any{"name": "filtered", "kind": "passthrough", "dist": "dist/filtered-y.tar.gz", "narHash": "sha256-BBB=", "lastModified": 1,
				"outputs": []string{"packages", "weird-name", "a b"}, "lock": json.RawMessage(lock)},
		},
	}
	data, _ := json.Marshal(b)
	must(os.WriteFile(filepath.Join(dir, "bundle.json"), data, 0o644))
	return dir
}

func newServer(t *testing.T, signed bool) *Server {
	var k *narinfo.SecretKey
	if signed {
		var err error
		if k, err = narinfo.ParseSecretKey(testKey); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{BundleDir: testBundle(t), Key: k, Logger: nil})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Logger.SetOutput(io.Discard)
	return s
}

func get(s *Server, method, host, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func untar(t *testing.T, data []byte) map[string]string {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
	return out
}

func TestNarinfoSigning(t *testing.T) {
	s := newServer(t, true)
	w := get(s, "GET", "a.example", "/cache/"+iaHash+".narinfo")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/x-nix-narinfo" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "\nSig: fiab-test-1:") {
		t.Fatalf("IA narinfo not signed:\n%s", w.Body.String())
	}
	w = get(s, "GET", "a.example", "/cache/"+caHash+".narinfo")
	if strings.Contains(w.Body.String(), "Sig:") {
		t.Fatal("CA narinfo signed")
	}
	if w := get(s, "GET", "a.example", "/cache/"+strings.Repeat("0", 32)+".narinfo"); w.Code != 404 {
		t.Fatalf("missing narinfo: %d", w.Code)
	}
	if w := get(newServer(t, false), "GET", "a.example", "/cache/"+iaHash+".narinfo"); strings.Contains(w.Body.String(), "Sig:") {
		t.Fatal("signed without a key")
	}
}

func TestHeadHasNoBody(t *testing.T) {
	s := newServer(t, true)
	for _, p := range []string{"/cache/" + iaHash + ".narinfo", "/cache/nar/abc.nar", "/flake.tar.gz", "/cache/nix-cache-info", "/dist/ca-rewrite-x.tar.gz"} {
		w := get(s, "HEAD", "a.example", p)
		if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
			t.Errorf("HEAD %s: code %d, body %d, length %q", p, w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
		}
	}
}

func TestStaticAnd404s(t *testing.T) {
	s := newServer(t, false)
	if w := get(s, "GET", "a.example", "/cache/nar/abc.nar"); w.Body.String() != "nar bytes" {
		t.Fatal("nar")
	}
	if w := get(s, "GET", "a.example", "/dist/ca-rewrite-x.tar.gz"); w.Body.String() != "ca dist" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("dist")
	}
	for _, p := range []string{"/cache/nar/../bundle.json", "/dist/../bundle.json", "/nope.tar.gz", "/cache/realisations/x.doi", "/bundle.json", "/dist/other.tar.gz"} {
		if w := get(s, "GET", "a.example", p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://a.example/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
}

func TestWrapperPerHost(t *testing.T) {
	s := newServer(t, true)
	a := get(s, "GET", "a.example:8080", "/flake.tar.gz")
	b := get(s, "GET", "b.example", "/ca-rewrite.tar.gz")
	if a.Code != 200 || b.Code != 200 {
		t.Fatal(a.Code, b.Code)
	}
	if !strings.Contains(a.Header().Get("Vary"), "Host") {
		t.Fatal("no Vary: Host")
	}
	fa := untar(t, a.Body.Bytes())
	nix := fa["flake/flake.nix"]
	for _, want := range []string{
		`extra-substituters = [ "http://a.example:8080/cache" ]`,
		`extra-trusted-public-keys = [ "fiab-test-1:RESI364QgvWTdorFzUYixApx+AsK1mqFPlKME3g9q/U=" ]`,
		`inputs.bundle.url = "http://a.example:8080/dist/ca-rewrite-x.tar.gz"`,
		`bundle.lib.mkOutputs { cacheUrl = "http://a.example:8080/cache"; }`,
		`description = "has \"quotes\" and \${interp} (ca-rewrite)"`,
	} {
		if !strings.Contains(nix, want) {
			t.Errorf("flake.nix lacks %s:\n%s", want, nix)
		}
	}
	if !strings.Contains(untar(t, b.Body.Bytes())["flake/flake.nix"], "http://b.example/cache") {
		t.Fatal("host b not templated")
	}
	// Immutable link resolves for the same host only.
	link := a.Header().Get("Link")
	if !strings.HasPrefix(link, "<http://a.example:8080/immutable/") || !strings.HasSuffix(link, `>; rel="immutable"`) {
		t.Fatalf("Link %q", link)
	}
	ipath := strings.TrimPrefix(strings.TrimSuffix(link, `>; rel="immutable"`), "<http://a.example:8080")
	if w := get(s, "GET", "a.example:8080", ipath); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), a.Body.Bytes()) {
		t.Fatalf("immutable same host: %d", w.Code)
	}
	if w := get(s, "GET", "b.example", ipath); w.Code != 404 {
		t.Fatalf("immutable other host: %d", w.Code)
	}
	// Deterministic.
	if again := get(s, "GET", "a.example:8080", "/flake.tar.gz"); !bytes.Equal(again.Body.Bytes(), a.Body.Bytes()) {
		t.Fatal("wrapper not deterministic")
	}
}

func TestPassthroughLockGraft(t *testing.T) {
	s := newServer(t, false)
	w := get(s, "GET", "c.example", "/filtered.tar.gz")
	files := untar(t, w.Body.Bytes())
	nix := files["flake/flake.nix"]
	for _, want := range []string{"packages = bundle.packages;", `weird-name = bundle.weird-name;`, `"a b" = bundle."a b";`} {
		if !strings.Contains(nix, want) {
			t.Errorf("missing %q in\n%s", want, nix)
		}
	}
	if strings.Contains(nix, "trusted-public-keys") {
		t.Error("key without signing")
	}
	var lock struct {
		Nodes map[string]map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(files["flake/flake.lock"]), &lock); err != nil {
		t.Fatal(err)
	}
	bundle := lock.Nodes["bundle"]
	ins := bundle["inputs"].(map[string]any)
	if ins["nixpkgs"] != "bundle_nixpkgs" || ins["utils"] != "bundle_utils" {
		t.Fatalf("bundle inputs %v", ins)
	}
	follows := lock.Nodes["bundle_utils"]["inputs"].(map[string]any)["nixpkgs"].([]any)
	if len(follows) != 2 || follows[0] != "bundle" || follows[1] != "nixpkgs" {
		t.Fatalf("follows %v", follows)
	}
	if _, ok := lock.Nodes["bundle_root"]; ok {
		t.Fatal("inner root copied")
	}
}

func TestHostValidation(t *testing.T) {
	s := newServer(t, false)
	for _, h := range []string{`evil"host`, "a.example/x", "${x}", "a..b", "-a.example"} {
		if w := get(s, "GET", h, "/flake.tar.gz"); w.Code != 400 {
			t.Errorf("host %q: %d", h, w.Code)
		}
	}
	for _, h := range []string{"localhost", "127.0.0.1:8080", "[::1]:80", "Cache.Example.org"} {
		if w := get(s, "GET", h, "/flake.tar.gz"); w.Code != 200 {
			t.Errorf("host %q: %d", h, w.Code)
		}
	}
	s.cfg.AllowedHosts = []string{"good.example"}
	if w := get(s, "GET", "bad.example", "/flake.tar.gz"); w.Code != 400 {
		t.Error("allowed-hosts not enforced")
	}
	if w := get(s, "GET", "good.example:9000", "/flake.tar.gz"); w.Code != 200 {
		t.Error("allowed host with port rejected")
	}
}

func TestForwardedAndBaseURL(t *testing.T) {
	s := newServer(t, false)
	s.cfg.TrustForwarded = true
	r := httptest.NewRequest("GET", "http://internal:8080/flake.tar.gz", nil)
	r.Header.Set("X-Forwarded-Host", "public.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if !strings.Contains(untar(t, w.Body.Bytes())["flake/flake.nix"], `"https://public.example/cache"`) {
		t.Fatal("forwarded headers ignored")
	}
	s2 := newServer(t, false)
	s2.cfg.BaseURL = "https://fixed.example/prefix/"
	w = get(s2, "GET", "whatever", "/flake.tar.gz")
	if !strings.Contains(untar(t, w.Body.Bytes())["flake/flake.nix"], `"https://fixed.example/prefix/cache"`) {
		t.Fatal("base URL ignored")
	}
}

var _ = http.StatusOK
