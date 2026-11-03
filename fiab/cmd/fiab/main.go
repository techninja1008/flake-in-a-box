// Command fiab is the flake-in-a-box tool: it rewrites closures into
// content-addressed binary caches, packs caches, detects used sources,
// computes NAR hashes and serves bundles.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/cachegen"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/detect"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/nar"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/narinfo"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/server"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/storepath"
)

const usage = `usage: fiab <command> [flags]

commands:
  serve        serve a bundle (wrapper flakes, dist tarballs, binary cache)
  rewrite      rewrite closures into a content-addressed binary cache
  pack-cache   merge flat/packed binary caches into one packed cache
  detect-srcs  list the files of a source tree used by a closure
  nar-hash     print the SRI NAR hash of a path
  sign         sign a narinfo read from stdin (input-addressed only)
  public-key   print the public key of a secret key file
`

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "rewrite":
		err = cmdRewrite(args)
	case "pack-cache":
		err = cmdPack(args)
	case "detect-srcs":
		err = cmdDetect(args)
	case "nar-hash":
		err = cmdNarHash(args)
	case "sign":
		err = cmdSign(args)
	case "public-key":
		err = cmdPublicKey(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "fiab: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("fiab %s: %v", cmd, err)
	}
}

func loadKey(file string) (*narinfo.SecretKey, error) {
	if file != "" {
		return narinfo.LoadSecretKey(file)
	}
	if f := os.Getenv("FIAB_SIGNING_KEY_FILE"); f != "" {
		return narinfo.LoadSecretKey(f)
	}
	if k := os.Getenv("FIAB_SIGNING_KEY"); k != "" {
		return narinfo.ParseSecretKey(k)
	}
	return nil, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	bundle := fs.String("bundle", os.Getenv("FIAB_BUNDLE"), "bundle directory (env FIAB_BUNDLE)")
	listen := fs.String("listen", envOr("FIAB_LISTEN", ":8080"), "listen address (env FIAB_LISTEN)")
	keyFile := fs.String("signing-key-file", "", "Nix secret key used to sign input-addressed narinfos (env FIAB_SIGNING_KEY_FILE, or the key itself in FIAB_SIGNING_KEY)")
	baseURL := fs.String("base-url", os.Getenv("FIAB_BASE_URL"), "fixed public base URL instead of one derived from the Host header (env FIAB_BASE_URL)")
	trustFwd := fs.Bool("trust-forwarded", os.Getenv("FIAB_TRUST_FORWARDED") == "1", "honour X-Forwarded-Host/-Proto (env FIAB_TRUST_FORWARDED=1)")
	allowed := fs.String("allowed-hosts", os.Getenv("FIAB_ALLOWED_HOSTS"), "comma-separated list of accepted hosts (env FIAB_ALLOWED_HOSTS)")
	var caches multiFlag
	fs.Var(&caches, "cache", "extra binary cache directory served under /cache/ (repeatable; with no --bundle, serve only caches)")
	fs.Parse(args)
	if *bundle == "" && len(caches) == 0 {
		return fmt.Errorf("--bundle (or --cache) is required")
	}
	key, err := loadKey(*keyFile)
	if err != nil {
		return fmt.Errorf("signing key: %w", err)
	}
	var hosts []string
	for _, h := range strings.Split(*allowed, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	srv, err := server.New(server.Config{
		BundleDir: *bundle, CacheDirs: caches, BaseURL: *baseURL, TrustForwarded: *trustFwd,
		AllowedHosts: hosts, Key: key, Logger: log.New(os.Stderr, "", log.LstdFlags),
	})
	if err != nil {
		return err
	}
	if key == nil {
		log.Printf("warning: no signing key; input-addressed narinfos are served unsigned")
	} else {
		log.Printf("signing input-addressed narinfos with %s", key.PublicKey())
	}
	log.Printf("listening on %s", *listen)
	hs := &http.Server{Addr: *listen, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	return hs.ListenAndServe()
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func cmdRewrite(args []string) error {
	fs := flag.NewFlagSet("rewrite", flag.ExitOnError)
	attrsFile := fs.String("attrs", os.Getenv("NIX_ATTRS_JSON_FILE"), "structured attrs JSON file")
	var attrs multiFlag
	fs.Var(&attrs, "attr", "closure attribute name (repeatable; default: closure)")
	out := fs.String("out", "", "output cache directory")
	mapFile := fs.String("map", "", "write old→new path mapping JSON here")
	comp := fs.String("compression", "zstd", "none, zstd or xz")
	jobs := fs.Int("jobs", runtime.NumCPU(), "parallel jobs")
	root := fs.String("root", "", "read store paths below this directory (for tests)")
	variantS := fs.String("variant", "positions", "self-reference hashing: positions (Nix >= 2.35) or legacy (Nix 2.17-2.34)")
	fs.Parse(args)
	variant, err := cachegen.ParseVariant(*variantS)
	if err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if len(attrs) == 0 {
		attrs = multiFlag{"closure"}
	}
	c, err := cachegen.GetCompressor(*comp)
	if err != nil {
		return err
	}
	items, err := cachegen.LoadClosure(*attrsFile, attrs)
	if err != nil {
		return err
	}
	remap, infos, err := cachegen.RewriteClosure(items, *out, c, *jobs, *root, variant)
	if err != nil {
		return err
	}
	if err := cachegen.WriteCacheInfo(*out, 0); err != nil {
		return err
	}
	if err := cachegen.WritePacked(*out, infos); err != nil {
		return err
	}
	if *mapFile != "" {
		b, _ := json.MarshalIndent(remap, "", "  ")
		if err := os.WriteFile(*mapFile, b, 0o644); err != nil {
			return err
		}
	}
	log.Printf("rewrote %d paths", len(remap))
	return nil
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack-cache", flag.ExitOnError)
	out := fs.String("out", "", "output directory")
	fs.Parse(args)
	if *out == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: fiab pack-cache --out DIR CACHE...")
	}
	return cachegen.Pack(*out, fs.Args())
}

func cmdDetect(args []string) error {
	fs := flag.NewFlagSet("detect-srcs", flag.ExitOnError)
	tree := fs.String("tree", "", "flake source tree")
	attrsFile := fs.String("attrs", os.Getenv("NIX_ATTRS_JSON_FILE"), "structured attrs JSON file")
	var attrs multiFlag
	fs.Var(&attrs, "attr", "closure attribute name (repeatable; default: closure)")
	fs.Parse(args)
	if len(attrs) == 0 {
		attrs = multiFlag{"closure"}
	}
	t, err := detect.Index(*tree)
	if err != nil {
		return err
	}
	items, err := cachegen.LoadClosure(*attrsFile, attrs)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, it := range items {
		if strings.HasSuffix(it.Path, ".drv") {
			continue
		}
		m, err := t.Match(it.Path)
		if err != nil {
			return err
		}
		if len(m) > 0 {
			log.Printf("%s: %d file(s) from the flake source", it.Path, len(m))
		}
		for _, f := range m {
			found[f] = true
		}
	}
	list := make([]string, 0, len(found))
	for f := range found {
		list = append(list, f)
	}
	sort.Strings(list)
	w := bufio.NewWriter(os.Stdout)
	for _, f := range list {
		fmt.Fprintln(w, f)
	}
	return w.Flush()
}

func cmdNarHash(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: fiab nar-hash PATH")
	}
	h := sha256.New()
	if err := nar.Dump(args[0], h); err != nil {
		return err
	}
	fmt.Println(storepath.FormatSRI(h.Sum(nil)))
	return nil
}

func cmdSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyFile := fs.String("key-file", "", "secret key file")
	fs.Parse(args)
	key, err := loadKey(*keyFile)
	if err != nil || key == nil {
		return fmt.Errorf("a signing key is required: %v", err)
	}
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	ni, err := narinfo.Parse(string(in))
	if err != nil {
		return err
	}
	ni, err = narinfo.SignIfNeeded(ni, key)
	if err != nil {
		return err
	}
	fmt.Print(ni.String())
	return nil
}

func cmdPublicKey(args []string) error {
	fs := flag.NewFlagSet("public-key", flag.ExitOnError)
	keyFile := fs.String("key-file", "", "secret key file")
	fs.Parse(args)
	key, err := loadKey(*keyFile)
	if err != nil || key == nil {
		return fmt.Errorf("a signing key is required: %v", err)
	}
	fmt.Println(key.PublicKey())
	return nil
}
