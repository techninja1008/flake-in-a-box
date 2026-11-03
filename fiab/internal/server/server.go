// Package server serves a flake-in-a-box bundle: per-host wrapper flakes,
// the static main flake tarballs, and the binary cache, signing
// input-addressed narinfos on the fly.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/techninja1008/flake-in-a-box/fiab/internal/cachegen"
	"github.com/techninja1008/flake-in-a-box/fiab/internal/narinfo"
)

// Config configures a Server.
type Config struct {
	BundleDir string
	// CacheDirs are extra binary cache directories (flat or packed) served
	// under /cache/. With no BundleDir, the server serves only these.
	CacheDirs []string
	// BaseURL, if set, overrides the URL derived from the request.
	BaseURL string
	// TrustForwarded honours X-Forwarded-Proto and X-Forwarded-Host.
	TrustForwarded bool
	// AllowedHosts, if non-empty, restricts accepted Host headers.
	AllowedHosts []string
	Key          *narinfo.SecretKey
	Logger       *log.Logger
}

// Server is an http.Handler.
type Server struct {
	cfg      Config
	bundle   *Bundle
	narinfos map[string]string // hash part -> narinfo text
	narDirs  map[string]string // nar file name -> cache dir
	signed   sync.Map          // hash part -> []byte
	wrappers sync.Map          // host|mode -> *wrapperTarball
	started  time.Time
}

type wrapperTarball struct {
	data []byte
	id   string
}

// wrapperMTime is the mtime of every entry in generated tarballs, and so
// the lastModified Nix records for them.
var wrapperMTime = time.Unix(1, 0)

// New loads the bundle and returns a server.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	var b *Bundle
	var dirs []string
	if cfg.BundleDir != "" {
		var err error
		if b, err = LoadBundle(cfg.BundleDir); err != nil {
			return nil, err
		}
		for _, c := range b.Caches {
			dirs = append(dirs, filepath.Join(cfg.BundleDir, c))
		}
	} else if len(cfg.CacheDirs) > 0 {
		b = &Bundle{Version: 1, Name: "binary cache"}
	} else {
		return nil, fmt.Errorf("a bundle or at least one cache directory is required")
	}
	dirs = append(dirs, cfg.CacheDirs...)
	s := &Server{cfg: cfg, bundle: b, narinfos: map[string]string{}, narDirs: map[string]string{}, started: time.Now()}
	for _, dir := range dirs {
		c := dir
		infos, err := cachegen.ReadAny(dir)
		if err != nil {
			return nil, fmt.Errorf("cache %s: %w", c, err)
		}
		for h, t := range infos {
			s.narinfos[h] = t
		}
		ents, err := os.ReadDir(filepath.Join(dir, "nar"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range ents {
			if _, dup := s.narDirs[e.Name()]; !dup {
				s.narDirs[e.Name()] = dir
			}
		}
	}
	cfg.Logger.Printf("bundle %q: %d modes, %d store paths, %d NAR files, signing %v",
		b.Name, len(b.Modes), len(s.narinfos), len(s.narDirs), cfg.Key != nil)
	return s, nil
}

var hostRE = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:.]+\](:[0-9]{1,5})?$`)

// baseURL derives the public base URL for a request.
func (s *Server) baseURL(r *http.Request) (string, error) {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/"), nil
	}
	host := r.Host
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if s.cfg.TrustForwarded {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = strings.TrimSpace(strings.Split(h, ",")[0])
		}
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			p = strings.ToLower(strings.TrimSpace(strings.Split(p, ",")[0]))
			if p == "http" || p == "https" {
				scheme = p
			}
		}
	}
	host = strings.ToLower(host)
	if !hostRE.MatchString(host) {
		return "", fmt.Errorf("invalid Host header %q", host)
	}
	if len(s.cfg.AllowedHosts) > 0 {
		h := host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			h = hh
		}
		ok := false
		for _, a := range s.cfg.AllowedHosts {
			if strings.EqualFold(a, host) || strings.EqualFold(a, h) {
				ok = true
			}
		}
		if !ok {
			return "", fmt.Errorf("host %q not allowed", host)
		}
	}
	return scheme + "://" + host, nil
}

func (s *Server) wrapper(base string, m *Mode) (*wrapperTarball, error) {
	key := base + "|" + m.Name
	if w, ok := s.wrappers.Load(key); ok {
		return w.(*wrapperTarball), nil
	}
	pk := ""
	if s.cfg.Key != nil {
		pk = s.cfg.Key.PublicKey()
	}
	p := WrapperParams{BaseURL: base, PublicKey: pk}
	lock, err := WrapperFlakeLock(s.bundle, m, p)
	if err != nil {
		return nil, err
	}
	data, err := TarGz("flake", map[string][]byte{
		"flake.nix":  []byte(WrapperFlakeNix(s.bundle, m, p)),
		"flake.lock": lock,
	}, wrapperMTime)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	w := &wrapperTarball{data: data, id: hex.EncodeToString(sum[:16])}
	if len(key) < 512 {
		s.wrappers.Store(key, w)
	}
	return w, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rw := &statusWriter{ResponseWriter: w, status: 200}
	start := time.Now()
	s.serve(rw, r)
	s.cfg.Logger.Printf("%s %s %s %d %s", r.Host, r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Microsecond))
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/":
		s.serveIndex(w, r)
	case p == "/public-key":
		if s.cfg.Key == nil {
			http.NotFound(w, r)
			return
		}
		writeBytes(w, r, "text/plain; charset=utf-8", []byte(s.cfg.Key.PublicKey()+"\n"), "")
	case strings.HasPrefix(p, "/cache/"):
		s.serveCache(w, r, strings.TrimPrefix(p, "/cache/"))
	case strings.HasPrefix(p, "/dist/"):
		s.serveDist(w, r, strings.TrimPrefix(p, "/dist/"))
	case strings.HasPrefix(p, "/immutable/"):
		s.serveImmutable(w, r, strings.TrimPrefix(p, "/immutable/"))
	case strings.HasSuffix(p, ".tar.gz") && strings.Count(p, "/") == 1:
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/"), ".tar.gz")
		if name == "flake" {
			name = s.bundle.DefaultMode
		}
		m := s.bundle.Mode(name)
		if m == nil {
			http.NotFound(w, r)
			return
		}
		s.serveWrapper(w, r, m)
	default:
		http.NotFound(w, r)
	}
}

func writeBytes(w http.ResponseWriter, r *http.Request, ctype string, data []byte, cacheControl string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	if cacheControl != "" {
		h.Set("Cache-Control", cacheControl)
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	base, err := s.baseURL(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "flake-in-a-box: %s\n", s.bundle.Name)
	if s.bundle.Description != "" {
		fmt.Fprintf(&b, "%s\n", s.bundle.Description)
	}
	fmt.Fprintf(&b, "\nBinary cache: %s/cache\n", base)
	if s.cfg.Key != nil {
		fmt.Fprintf(&b, "Public key:   %s\n", s.cfg.Key.PublicKey())
	}
	b.WriteString("\nFlakes:\n")
	for _, m := range s.bundle.Modes {
		def := ""
		if m.Name == s.bundle.DefaultMode {
			def = fmt.Sprintf("   (also %s/flake.tar.gz)", base)
		}
		fmt.Fprintf(&b, "  %-11s %s/%s.tar.gz%s\n", m.Name, base, m.Name, def)
		var notes []string
		if m.NeedsFetchClosure {
			notes = append(notes, "needs --extra-experimental-features fetch-closure")
		}
		if m.Signed {
			notes = append(notes, "needs the public key above trusted")
		} else {
			notes = append(notes, "no trusted key needed")
		}
		fmt.Fprintf(&b, "              (%s)\n", strings.Join(notes, "; "))
	}
	fmt.Fprintf(&b, "\nExample:\n  nix build --accept-flake-config '%s/flake.tar.gz#<package>'\n", base)
	writeBytes(w, r, "text/plain; charset=utf-8", []byte(b.String()), "no-cache")
}

func (s *Server) serveWrapper(w http.ResponseWriter, r *http.Request, m *Mode) {
	base, err := s.baseURL(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t, err := s.wrapper(base, m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Vary", "Host, X-Forwarded-Host, X-Forwarded-Proto")
	h.Set("Link", fmt.Sprintf("<%s/immutable/%s/%s.tar.gz>; rel=\"immutable\"", base, t.id, m.Name))
	h.Set("ETag", `"`+t.id+`"`)
	writeBytes(w, r, "application/gzip", t.data, "no-cache")
}

func (s *Server) serveImmutable(w http.ResponseWriter, r *http.Request, rest string) {
	id, file, ok := strings.Cut(rest, "/")
	if !ok || !strings.HasSuffix(file, ".tar.gz") || strings.Contains(file, "/") {
		http.NotFound(w, r)
		return
	}
	m := s.bundle.Mode(strings.TrimSuffix(file, ".tar.gz"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	base, err := s.baseURL(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t, err := s.wrapper(base, m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t.id != id {
		http.Error(w, "this immutable wrapper was generated for a different host or bundle", http.StatusNotFound)
		return
	}
	w.Header().Set("Vary", "Host, X-Forwarded-Host, X-Forwarded-Proto")
	w.Header().Set("ETag", `"`+t.id+`"`)
	writeBytes(w, r, "application/gzip", t.data, "public, max-age=31536000, immutable")
}

func (s *Server) serveDist(w http.ResponseWriter, r *http.Request, name string) {
	for _, m := range s.bundle.Modes {
		if m.Dist == "dist/"+name {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Header().Set("Content-Type", "application/gzip")
			http.ServeFile(w, r, filepath.Join(s.cfg.BundleDir, m.Dist))
			return
		}
	}
	http.NotFound(w, r)
}

var narinfoRE = regexp.MustCompile(`^([0-9a-df-np-sv-z]{32})\.narinfo$`)

func (s *Server) serveCache(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case rest == "nix-cache-info":
		writeBytes(w, r, "text/x-nix-cache-info", []byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"), "public, max-age=3600")
	case narinfoRE.MatchString(rest):
		hp := narinfoRE.FindStringSubmatch(rest)[1]
		body, err := s.narinfo(hp)
		if err != nil {
			s.cfg.Logger.Printf("narinfo %s: %v", hp, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if body == nil {
			w.Header().Set("Cache-Control", "public, max-age=60")
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeBytes(w, r, "text/x-nix-narinfo", body, "public, max-age=3600")
	case strings.HasPrefix(rest, "nar/"):
		name := strings.TrimPrefix(rest, "nar/")
		dir, ok := s.narDirs[name]
		if !ok || name != path.Base(name) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-nix-nar")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(dir, "nar", name))
	default:
		http.NotFound(w, r)
	}
}

// narinfo returns the (signed, where applicable) narinfo, or nil.
func (s *Server) narinfo(hashPart string) ([]byte, error) {
	if v, ok := s.signed.Load(hashPart); ok {
		return v.([]byte), nil
	}
	text, ok := s.narinfos[hashPart]
	if !ok {
		return nil, nil
	}
	ni, err := narinfo.Parse(text)
	if err != nil {
		return nil, err
	}
	ni, err = narinfo.SignIfNeeded(ni, s.cfg.Key)
	if err != nil {
		return nil, err
	}
	out := []byte(ni.String())
	s.signed.Store(hashPart, out)
	return out, nil
}
