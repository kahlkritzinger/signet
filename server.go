package main

// The editor's local server. It listens on 127.0.0.1 only, and every request
// for your data must prove it comes from you:
//
//   - the CLI sends the secret in ~/.signet/secret (readable only by you), and
//     before it sends anything it makes the server prove it knows the secret
//     too, so another program on the port can't pretend to be Signet;
//   - the editor window is opened with a one-time launch code, swapped for a
//     session cookie that scripts can't read.
//
// Other accounts on the same computer, and websites you visit, get nothing.
// On a server behind Cloudflare Access, the signed Access login counts too
// (see access.go).
//
//	GET  /healthz?nonce=N            {"ok", "app": "signet", "version", "proof": HMAC(secret, N)}
//	POST /api/launch                 a one-time code for opening a window (CLI only)
//	GET  /?k=CODE&doc=...            swaps the code for a session, then shows the editor
//	GET  /api/store/{bank|profile}   your signature bank / profile (JSON)
//	PUT  /api/store/{bank|profile}    X-Signet-If-Version: refuse (409) if it changed since
//	GET|PUT /api/sync/{bank|profile}  the same, for your other machines (see account.go)
//	POST /api/docs?name=&path=       hand a PDF to the editor -> {"id": ...}
//	GET  /api/doc/{id}               the PDF (name in X-Name)
//	POST /api/save/{id}              write <name>-signed.pdf next to the original
//	GET  /                           the editor (its files hold no data)

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

const (
	maxPDF      = 100 << 20
	maxStore    = 20 << 20
	docTTL      = 48 * time.Hour
	codeTTL     = 2 * time.Minute
	maxSessions = 64
	cookieName  = "signet"

	versionHeader   = "X-Signet-Version"    // which saved state you got (Cloudflare rewrites ETags, so not those)
	ifVersionHeader = "X-Signet-If-Version" // only save if it is still this one
)

var docID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

const csp = "default-src 'none'; script-src 'self'; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data: blob:; connect-src 'self' data: blob:; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

type server struct {
	dir      string
	secret   []byte
	access   *accessAuth     // set when running on a server behind Cloudflare Access
	hosts    map[string]bool // extra hostnames allowed besides localhost
	mu       sync.Mutex
	sessions map[string]time.Time // session cookie -> created
	codes    map[string]time.Time // one-time launch code -> expires
}

type docMeta struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`  // where the original lives, when opened from disk
	Saved string `json:"saved,omitempty"` // where we saved it last, so saving again overwrites our own file
}

func newServer(dir string) (http.Handler, error) {
	secret, err := loadSecret(dir)
	if err != nil {
		return nil, err
	}
	access, err := accessFromEnv()
	if err != nil {
		return nil, err
	}
	s := &server{dir: dir, secret: secret, access: access, hosts: map[string]bool{},
		sessions: map[string]time.Time{}, codes: map[string]time.Time{}}
	for _, h := range strings.Split(os.Getenv("SIGNET_HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			s.hosts[h] = true
		}
	}
	if len(s.hosts) > 0 && s.access == nil {
		return nil, fmt.Errorf("SIGNET_HOSTS lets other addresses in, so a login is needed: set SIGNET_ACCESS_TEAM, SIGNET_ACCESS_AUD and SIGNET_ACCESS_EMAIL")
	}
	web, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(web)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /api/launch", s.cliOnly(s.launch))
	mux.HandleFunc("GET /api/store/{key}", s.authed(s.storeGet))
	mux.HandleFunc("PUT /api/store/{key}", s.authed(s.storePut))
	mux.HandleFunc("GET /api/sync/{key}", s.syncAuthed(s.storeGet)) // your other machines (account.go)
	mux.HandleFunc("PUT /api/sync/{key}", s.syncAuthed(s.storePut))
	mux.HandleFunc("POST /api/docs", s.authed(s.docUpload))
	mux.HandleFunc("GET /api/doc/{id}", s.authed(s.docGet))
	mux.HandleFunc("POST /api/save/{id}", s.authed(s.docSave))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.URL.Query().Has("k") {
			s.redeem(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	return s.guard(mux), nil
}

// loadSecret reads ~/.signet/secret, making it (readable only by you) the first time.
func loadSecret(dir string) ([]byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "secret")
	b, err := os.ReadFile(p)
	if err == nil && len(bytes.TrimSpace(b)) >= 32 {
		return bytes.TrimSpace(b), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	// Create it exclusively: if two Signets start at once, the first one wins
	// and the other reads its secret.
	tmp := p + "." + newID() + ".tmp"
	if err := writeAtomic(tmp, []byte(hex.EncodeToString(raw)), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, p); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	b, err = os.ReadFile(p)
	if err != nil || len(bytes.TrimSpace(b)) < 32 {
		return nil, fmt.Errorf("could not read %s", p)
	}
	return bytes.TrimSpace(b), nil
}

func proof(secret []byte, nonce string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("signet-proof:" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// guard applies to everything: only requests addressed to this machine (blocks
// DNS rebinding), no writes from other websites, strict browser headers, and a
// panic in one request never takes the editor down.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				writeJSON(w, 500, map[string]string{"error": "internal error"})
			}
		}()
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !localHost(r.Host) && !s.hosts[hostOnly(r.Host)] {
			writeJSON(w, 403, map[string]string{"error": "local only"})
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			writeJSON(w, 403, map[string]string{"error": "wrong origin"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					writeJSON(w, 403, map[string]string{"error": "wrong origin"})
					return
				}
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}

func localHost(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

func (s *server) cliOK(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(tok), s.secret) == 1
}

func (s *server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[c.Value]
	return ok
}

func (s *server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cliOK(r) && !s.sessionOK(r) && !(s.access != nil && s.access.email(r) != "") {
			writeJSON(w, 401, map[string]string{"error": "not signed in: open the PDF with signet again"})
			return
		}
		h(w, r)
	}
}

// syncAuthed lets in your other machines: the CLI secret, or (behind Cloudflare
// Access) a service token you allowed in SIGNET_ACCESS_SYNC_CLIENT.
func (s *server) syncAuthed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cliOK(r) && !(s.access != nil && s.access.syncClient(r)) {
			writeJSON(w, 401, map[string]string{"error": "this machine is not allowed to sync"})
			return
		}
		h(w, r)
	}
}

func (s *server) cliOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cliOK(r) {
			writeJSON(w, 401, map[string]string{"error": "not allowed"})
			return
		}
		h(w, r)
	}
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	res := map[string]any{"ok": true, "app": "signet", "version": version}
	if n := r.URL.Query().Get("nonce"); n != "" && len(n) <= 128 {
		res["proof"] = proof(s.secret, n)
	}
	writeJSON(w, 200, res)
}

func (s *server) launch(w http.ResponseWriter, r *http.Request) {
	code := newID()
	s.mu.Lock()
	now := time.Now()
	for c, exp := range s.codes {
		if now.After(exp) {
			delete(s.codes, c)
		}
	}
	s.codes[code] = now.Add(codeTTL)
	s.mu.Unlock()
	writeJSON(w, 200, map[string]string{"code": code})
}

// redeem swaps a one-time launch code for a session cookie, then drops the
// code from the address bar.
func (s *server) redeem(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("k")
	q.Del("k")
	s.mu.Lock()
	exp, ok := s.codes[code]
	delete(s.codes, code) // single use, valid or not
	if ok && time.Now().Before(exp) {
		if len(s.sessions) >= maxSessions { // forget the oldest window
			var oldest string
			var t time.Time
			for k, v := range s.sessions {
				if oldest == "" || v.Before(t) {
					oldest, t = k, v
				}
			}
			delete(s.sessions, oldest)
		}
		sess := newID() + newID()
		s.sessions[sess] = time.Now()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: sess, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	s.mu.Unlock()
	target := "/"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *server) storeGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !storeKeys[key] {
		writeJSON(w, 404, map[string]string{"error": "unknown"})
		return
	}
	b, err := readStore(s.dir, key)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set(versionHeader, storeVersion(b))
	if b == nil {
		b = []byte("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (s *server) storePut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !storeKeys[key] {
		writeJSON(w, 404, map[string]string{"error": "unknown"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStore))
	if err != nil || len(body) == 0 {
		writeJSON(w, 413, map[string]string{"error": "empty or too big"})
		return
	}
	if err := checkStore(key, body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	v, err := writeStore(s.dir, key, body, r.Header.Get(ifVersionHeader))
	if errors.Is(err, errChanged) {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set(versionHeader, v)
	writeJSON(w, 200, true)
}

// checkStore refuses data the editor would never send, so a bug can't wipe a good bank.
func checkStore(key string, body []byte) error {
	switch key {
	case "bank":
		var marks []Mark
		if err := json.Unmarshal(body, &marks); err != nil {
			return fmt.Errorf("bank must be a list of marks")
		}
	case "profile":
		var p Profile
		if err := json.Unmarshal(body, &p); err != nil {
			return fmt.Errorf("profile must have groups of fields")
		}
	}
	return nil
}

func (s *server) docsDir() string {
	d := filepath.Join(s.dir, "docs")
	os.MkdirAll(d, 0o700)
	return d
}

func (s *server) prune() {
	entries, _ := os.ReadDir(s.docsDir())
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > docTTL {
			os.Remove(filepath.Join(s.docsDir(), e.Name()))
		}
	}
}

func (s *server) docUpload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPDF))
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "too big"})
		return
	}
	if !bytes.HasPrefix(body, []byte("%PDF")) {
		writeJSON(w, 400, map[string]string{"error": "not a PDF"})
		return
	}
	s.prune()
	name := filepath.Base(r.URL.Query().Get("name"))
	if name == "." || name == "/" || name == "" {
		name = "document.pdf"
	}
	meta := docMeta{Name: name}
	if p := r.URL.Query().Get("path"); p != "" && filepath.IsAbs(p) && s.cliOK(r) {
		meta.Path = filepath.Clean(p) // only the CLI may say where a file lives on disk
	}
	id := newID()
	if err := writeAtomic(filepath.Join(s.docsDir(), id+".pdf"), body, 0o600); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	s.saveMeta(id, meta)
	writeJSON(w, 200, map[string]string{"id": id})
}

func (s *server) meta(id string) (docMeta, bool) {
	var m docMeta
	if !docID.MatchString(id) {
		return m, false
	}
	b, err := os.ReadFile(filepath.Join(s.docsDir(), id+".json"))
	if err != nil || json.Unmarshal(b, &m) != nil {
		return m, false
	}
	return m, true
}

func (s *server) saveMeta(id string, m docMeta) {
	b, _ := json.Marshal(m)
	writeAtomic(filepath.Join(s.docsDir(), id+".json"), b, 0o600)
}

func (s *server) docGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, ok := s.meta(id)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "gone"})
		return
	}
	b, err := os.ReadFile(filepath.Join(s.docsDir(), id+".pdf"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "gone"})
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Name", url.PathEscape(m.Name))
	w.Write(b)
}

func (s *server) docSave(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, ok := s.meta(id)
	if !ok || m.Path == "" {
		writeJSON(w, 404, map[string]string{"error": "no original on disk"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPDF))
	if err != nil || !bytes.HasPrefix(body, []byte("%PDF")) {
		writeJSON(w, 400, map[string]string{"error": "not a PDF"})
		return
	}
	out := m.Saved
	if out == "" || !exists(out) {
		out = freeName(m.Path)
	}
	if out == m.Path {
		writeJSON(w, 500, map[string]string{"error": "refusing to overwrite the original"})
		return
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(m.Path); err == nil {
		mode = info.Mode().Perm() // the signed copy is as private as the original
	}
	if err := writeAtomic(out, body, mode); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not write " + out + ": " + err.Error()})
		return
	}
	m.Saved = out
	s.saveMeta(id, m)
	writeJSON(w, 200, map[string]string{"path": out})
}

// freeName: <name>-signed.pdf next to the original, or -signed-2, -3 ... if taken.
func freeName(original string) string {
	base := strings.TrimSuffix(original, filepath.Ext(original)) + "-signed"
	out := base + ".pdf"
	for n := 2; exists(out); n++ {
		out = base + "-" + itoa(n) + ".pdf"
	}
	return out
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func newID() string {
	b := make([]byte, 18)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Print(err)
	}
}
