package main

// The editor's local server. It only listens on 127.0.0.1 and only answers
// requests addressed to localhost, so websites you visit can't reach it.
//
//	GET  /healthz                    {"ok": true, "app": "signet"}
//	GET  /api/store/{bank|profile}   your signature bank / profile (JSON)
//	PUT  /api/store/{bank|profile}
//	POST /api/docs?name=&path=       hand a PDF to the editor -> {"id": ...}
//	GET  /api/doc/{id}               the PDF (name in X-Name)
//	POST /api/save/{id}              write <name>-signed.pdf next to the original
//	GET  /                           the editor

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

const (
	maxPDF   = 100 << 20
	maxStore = 20 << 20
	docTTL   = 48 * time.Hour
)

var docID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

type server struct{ dir string }

type docMeta struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`  // where the original lives, when opened from disk
	Saved string `json:"saved,omitempty"` // where we saved it last, so saving again overwrites our own file
}

func newServer(dir string) http.Handler {
	s := &server{dir: dir}
	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "app": "signet", "version": version})
	})
	mux.HandleFunc("GET /api/store/{key}", s.storeGet)
	mux.HandleFunc("PUT /api/store/{key}", s.storePut)
	mux.HandleFunc("POST /api/docs", s.docUpload)
	mux.HandleFunc("GET /api/doc/{id}", s.docGet)
	mux.HandleFunc("POST /api/save/{id}", s.docSave)
	mux.Handle("GET /", http.FileServerFS(web))
	return gate(mux)
}

// gate: only requests addressed to this machine (blocks DNS rebinding), and
// writes only from this page (blocks other websites posting to us).
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			writeJSON(w, 403, map[string]string{"error": "local only"})
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
		next.ServeHTTP(w, r)
	})
}

func localHost(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
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
	if b == nil {
		b = []byte("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *server) storePut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !storeKeys[key] {
		writeJSON(w, 404, map[string]string{"error": "unknown"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxStore+1))
	if err != nil || len(body) == 0 || len(body) > maxStore {
		writeJSON(w, 413, map[string]string{"error": "empty or too big"})
		return
	}
	if err := writeStore(s.dir, key, body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, true)
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
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPDF+1))
	if err != nil || len(body) > maxPDF {
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
	if p := r.URL.Query().Get("path"); p != "" && filepath.IsAbs(p) {
		meta.Path = p
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
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *server) docSave(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, ok := s.meta(id)
	if !ok || m.Path == "" {
		writeJSON(w, 404, map[string]string{"error": "no original on disk"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPDF+1))
	if err != nil || len(body) > maxPDF || !bytes.HasPrefix(body, []byte("%PDF")) {
		writeJSON(w, 400, map[string]string{"error": "not a PDF"})
		return
	}
	out := m.Saved
	if out == "" {
		out = freeName(m.Path)
	}
	if err := writeAtomic(out, body, 0o644); err != nil {
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

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

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
