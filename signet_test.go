package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

const sample = "testdata/sample.pdf"

// fakeMark: a black stroke on transparent, stored like the editor stores it.
func fakeMark(w, h int) string {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 5; x < w-5; x++ {
		for d := -2; d <= 2; d++ {
			img.Set(x, h/2+d, color.NRGBA{0, 0, 0, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func testData(t *testing.T) string {
	dir := t.TempDir()
	bank, _ := json.Marshal([]Mark{{ID: "s1", Kind: "sig", Mask: fakeMark(300, 100), Ar: 3}, {ID: "i1", Kind: "ini", Mask: fakeMark(80, 80), Ar: 1}})
	prof := `{"groups":[{"name":"Me","fields":[{"key":"first","label":"First name","value":"Ann"},{"key":"sur","label":"Surname","value":"Smith"},
	{"key":"id","label":"ID number","value":"800101"},{"key":"street","label":"Street name","value":"Long St","addr":true},
	{"key":"city","label":"City","value":"Cape Town","addr":true}]},{"name":"Car","fields":[{"label":"Registration","value":"CA 123"}]}]}`
	_, err := writeLocal(dir, "bank", bank, "")
	must(err)
	_, err = writeLocal(dir, "profile", []byte(prof), "")
	must(err)
	return dir
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func TestProfileEntries(t *testing.T) {
	p, err := loadProfile(testData(t))
	if err != nil {
		t.Fatal(err)
	}
	e := profileEntries(p)
	for label, want := range map[string]string{"Full name": "Ann Smith", "Initials & surname": "A Smith", "address": "Long St, Cape Town", "Car: Registration": "CA 123", "ID number": "800101"} {
		if got, err := lookup(e, label); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", label, got, err, want)
		}
	}
	if _, err := lookup(e, "Passport"); err == nil || !strings.Contains(err.Error(), "profile has no") {
		t.Errorf("missing label: %v", err)
	}
	if _, err := lookup(nil, "Full name"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty profile: %v", err)
	}
}

func TestInspectListsFieldsWithPositions(t *testing.T) {
	md, err := inspect(sample, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 pages, 595x842 pt", "| tenant.name | text | 1 | 159.5 | 141.5 | 221 | 21 |", "| term | dropdown | 1 |", "6 months, 12 months", "Sample Lease Agreement - page 1", "2 more pages"} {
		if !strings.Contains(md, want) {
			t.Errorf("inspect is missing %q:\n%s", want, md)
		}
	}
}

func TestFindGivesPositions(t *testing.T) {
	hits, err := find(sample, "initial here")
	if err != nil || len(hits) != 3 {
		t.Fatalf("hits %v, %v", hits, err)
	}
	h := hits[0]
	if h.Page != 1 || h.X < 438 || h.X > 442 || h.Y < 776 || h.Y > 786 || h.W < 40 || h.W > 52 {
		t.Errorf("Initial here at %+v; expected about x 440, y 781, w 46", h)
	}
}

func TestApplyFillsSignsAndInitialsEveryPage(t *testing.T) {
	dir := testData(t)
	out := filepath.Join(t.TempDir(), "out.pdf")
	var plan Plan
	must(json.Unmarshal([]byte(`{"fields":{"tenant.name":{"profile":"Full name"},"agree":true,"term":"12 months"},
	 "items":[{"type":"sign","page":1,"x":115,"y":680},{"type":"date","page":1,"x":290,"y":712,"date":"2026-11-15"},
	 {"type":"text","page":1,"x":330,"y":295,"profile":"ID number"},{"type":"initial","page":"every","x":490,"y":768},
	 {"type":"check","page":2,"x":60,"y":60},{"type":"cross","page":2,"x":80,"y":60,"ink":"red"}]}`), &plan))
	res, err := apply(sample, plan, out, dir, rand.New(rand.NewPCG(1, 2)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Saved != out || len(res.Done) != 9 {
		t.Errorf("result %+v", res)
	}
	b, _ := os.ReadFile(out)
	ff, err := api.FormFields(bg, bytes.NewReader(b), conf())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range ff {
		got[f.Name] = f.V
	}
	if got["tenant.name"] != "Ann Smith" || got["term"] != "12 months" || (got["agree"] != "Yes" && got["agree"] != "true") {
		t.Errorf("fields after apply: %v", got) // still live fields, not flattened
	}
	if ok, err := api.HasWatermarks(bg, bytes.NewReader(b), conf()); err != nil || !ok {
		t.Errorf("no marks added: %v", err) // positions are checked by eye: see CONTRIBUTING
	}
}

func TestApplyDefaultsAndErrors(t *testing.T) {
	dir := testData(t)
	src := filepath.Join(t.TempDir(), "Lease.pdf")
	b, _ := os.ReadFile(sample)
	os.WriteFile(src, b, 0o644)
	r := rand.New(rand.NewPCG(1, 2))
	res, err := apply(src, Plan{}, "", dir, r)
	if err != nil || !strings.HasSuffix(res.Saved, "Lease-signed.pdf") {
		t.Errorf("default name: %v %v", res, err)
	}
	cases := map[string]string{
		`{"fields":{"nope":"x"}}`:                                               "no form field named",
		`{"items":[{"type":"sign","page":9,"x":0,"y":0}]}`:                      "page 9 is not in",
		`{"items":[{"type":"sign","page":1,"x":0,"y":0,"ink":"green"}]}`:        "ink must be",
		`{"items":[{"type":"stamp","page":1,"x":0,"y":0}]}`:                     "unknown item type",
		`{"items":[{"type":"text","page":1,"x":0,"y":0,"profile":"Passport"}]}`: "profile has no",
	}
	for js, want := range cases {
		var p Plan
		must(json.Unmarshal([]byte(js), &p))
		if _, err := apply(sample, p, filepath.Join(t.TempDir(), "x.pdf"), dir, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", js, err, want)
		}
	}
	if _, err := apply(sample, Plan{Items: []Item{{Type: "sign", Page: 1.0}}}, filepath.Join(t.TempDir(), "x.pdf"), t.TempDir(), r); err == nil || !strings.Contains(err.Error(), "no signature in your bank") {
		t.Errorf("empty bank: %v", err)
	}
}

type client struct {
	t      *testing.T
	h      http.Handler
	secret string
	cookie *http.Cookie
}

const local = "127.0.0.1:7766"

func newClient(t *testing.T, dir string) *client {
	h, err := newServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := os.ReadFile(filepath.Join(dir, "secret"))
	return &client{t: t, h: h, secret: string(secret)}
}

// do sends a request; auth is "cli" (the secret), "window" (session cookie) or "" (nothing).
func (c *client) do(method, target, auth string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Host = local
	switch auth {
	case "cli":
		r.Header.Set("Authorization", "Bearer "+c.secret)
	case "window":
		if c.cookie != nil {
			r.AddCookie(c.cookie)
		}
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	return w
}

func TestOnlyYouCanReachYourData(t *testing.T) {
	dir := t.TempDir()
	c := newClient(t, dir)
	if info, _ := os.Stat(filepath.Join(dir, "secret")); info == nil || (info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("secret missing or readable by others: %v", info)
	}
	if w := c.do("GET", "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Signet</title>") {
		t.Errorf("the page itself (no data in it) should load: %d", w.Code)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
		if c.do("GET", "/", "", nil).Header().Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
	// nobody without the secret or a session: other accounts on this computer
	for _, path := range []string{"/api/store/bank", "/api/store/profile", "/api/doc/abcdefghijklmnopqrst"} {
		if w := c.do("GET", path, "", nil); w.Code != 401 {
			t.Errorf("%s without auth: %d", path, w.Code)
		}
	}
	if w := c.do("POST", "/api/launch", "", nil); w.Code != 401 {
		t.Errorf("launch without the secret: %d", w.Code)
	}
	if w := c.do("GET", "/api/store/bank", "", nil, "Authorization", "Bearer wrong"); w.Code != 401 {
		t.Errorf("wrong secret: %d", w.Code)
	}
	// websites: DNS rebinding, cross-site requests, foreign origins
	if w := c.do("GET", "/api/store/bank", "cli", nil, "Host", "evil.example:7766"); w.Code != 403 {
		t.Errorf("DNS rebinding not blocked: %d", w.Code)
	}
	if w := c.do("GET", "/api/store/bank", "cli", nil, "Sec-Fetch-Site", "cross-site"); w.Code != 403 {
		t.Errorf("cross-site fetch not blocked: %d", w.Code)
	}
	if w := c.do("PUT", "/api/store/bank", "cli", []byte("[]"), "Origin", "https://evil.example"); w.Code != 403 {
		t.Errorf("foreign origin not blocked: %d", w.Code)
	}
	// the CLI, with the secret
	if w := c.do("GET", "/api/store/bank", "cli", nil); w.Code != 200 || w.Body.String() != "null" {
		t.Errorf("CLI read: %d %q", w.Code, w.Body.String())
	}
}

func TestWindowsGetInWithAOneTimeCode(t *testing.T) {
	c := newClient(t, t.TempDir())
	var launch struct{ Code string }
	json.Unmarshal(c.do("POST", "/api/launch", "cli", nil).Body.Bytes(), &launch)
	w := c.do("GET", "/?k="+launch.Code+"&doc=abc", "", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/?doc=abc" {
		t.Fatalf("redeem: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v", cookies)
	}
	c.cookie = cookies[0]
	if w := c.do("GET", "/api/store/bank", "window", nil); w.Code != 200 {
		t.Errorf("window with session: %d", w.Code)
	}
	if w := c.do("GET", "/?k="+launch.Code, "", nil); len(w.Result().Cookies()) != 0 {
		t.Error("a launch code worked twice")
	}
	if w := c.do("GET", "/?k=madeup", "", nil); len(w.Result().Cookies()) != 0 {
		t.Error("a made-up code gave a session")
	}
	// a window may not choose where files are written: only the CLI can
	pdf, _ := os.ReadFile(sample)
	var up struct{ ID string }
	json.Unmarshal(c.do("POST", "/api/docs?path=/etc/passwd", "window", pdf).Body.Bytes(), &up)
	if w := c.do("POST", "/api/save/"+up.ID, "window", pdf); w.Code != 404 {
		t.Errorf("a window chose where to save: %d", w.Code)
	}
}

func TestDocumentsAndSaving(t *testing.T) {
	c := newClient(t, t.TempDir())
	pdf, _ := os.ReadFile(sample)
	orig := filepath.Join(t.TempDir(), "Lease.pdf")
	os.WriteFile(orig, pdf, 0o600)
	up := func(path string) string {
		w := c.do("POST", "/api/docs?name=Lease.pdf&path="+url.QueryEscape(path), "cli", pdf)
		var r struct{ ID string }
		json.Unmarshal(w.Body.Bytes(), &r)
		return r.ID
	}
	save := func(id string, body []byte) (int, string) {
		w := c.do("POST", "/api/save/"+id, "cli", body)
		var r struct{ Path string }
		json.Unmarshal(w.Body.Bytes(), &r)
		return w.Code, r.Path
	}
	id := up(orig)
	if w := c.do("GET", "/api/doc/"+id, "cli", nil); w.Code != 200 || w.Header().Get("X-Name") != "Lease.pdf" {
		t.Errorf("get doc: %d %q", w.Code, w.Header().Get("X-Name"))
	}
	if w := c.do("POST", "/api/docs", "cli", []byte("hello")); w.Code != 400 {
		t.Errorf("non-PDF accepted: %d", w.Code)
	}
	_, first := save(id, pdf)
	if first != filepath.Join(filepath.Dir(orig), "Lease-signed.pdf") {
		t.Errorf("first save went to %q", first)
	}
	if info, _ := os.Stat(first); info == nil || (os.PathSeparator == '/' && info.Mode().Perm() != 0o600) {
		t.Errorf("signed copy should be as private as the original: %v", info)
	}
	if _, again := save(id, pdf); again != first {
		t.Errorf("saving again should overwrite our own file, went to %q", again)
	}
	if _, other := save(up(orig), pdf); !strings.HasSuffix(other, "Lease-signed-2.pdf") {
		t.Errorf("a second document must not overwrite the first one's file: %q", other)
	}
	if code, _ := save(id, []byte("nope")); code != 400 {
		t.Errorf("non-PDF saved: %d", code)
	}
	if code, _ := save("abc", pdf); code != 404 {
		t.Errorf("bad id: %d", code)
	}
	if b, _ := os.ReadFile(orig); !bytes.Equal(b, pdf) {
		t.Error("the original changed")
	}
}

func TestStoreKeepsBackupsAndHealsItself(t *testing.T) {
	dir := t.TempDir()
	c := newClient(t, dir)
	for i := 0; i < 14; i++ {
		body := []byte(`[{"id":"m` + itoa(i) + `","kind":"sig","mask":"data:,x","ar":1}]`)
		if w := c.do("PUT", "/api/store/bank", "cli", body); w.Code != 200 {
			t.Fatalf("save %d: %d", i, w.Code)
		}
	}
	if n := len(backups(dir, "bank")); n != keepBackups {
		t.Errorf("%d backups, want %d", n, keepBackups)
	}
	if w := c.do("PUT", "/api/store/bank", "cli", []byte(`{"not":"a list"}`)); w.Code != 400 {
		t.Errorf("a non-list bank was accepted: %d", w.Code)
	}
	os.WriteFile(storePath(dir, "bank"), []byte(`[{"id":"m13", "kind":`), 0o600) // damaged, e.g. a full disk
	w := c.do("GET", "/api/store/bank", "cli", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"m12"`) {
		t.Errorf("not restored from the newest backup: %d %s", w.Code, w.Body.String())
	}
	if b, _ := os.ReadFile(storePath(dir, "bank")); !json.Valid(b) {
		t.Error("damaged file not replaced")
	}
	damaged, _ := filepath.Glob(storePath(dir, "bank") + ".damaged-*")
	if len(damaged) != 1 {
		t.Errorf("the damaged file should be kept aside: %v", damaged)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestTheCLIOnlyTrustsTheRealEditor(t *testing.T) {
	dir := t.TempDir()
	secret, _ := loadSecret(dir)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"app":"signet","proof":"0000"}`)) // something else squatting the port
	}))
	defer fake.Close()
	if verified(fake.URL, secret) {
		t.Fatal("trusted a fake editor")
	}
	h, _ := newServer(dir)
	real := httptest.NewServer(h)
	defer real.Close()
	if !verified(strings.Replace(real.URL, "127.0.0.1", "localhost", 1), secret) && !verified(real.URL, secret) {
		t.Fatal("did not trust the real editor")
	}
	other, _ := loadSecret(t.TempDir()) // another account's Signet
	if verified(real.URL, other) {
		t.Fatal("trusted another account's editor")
	}
}

func TestServeFindsAFreePortAndRecordsIt(t *testing.T) {
	dir := t.TempDir()
	busy := httptest.NewServer(http.NotFoundHandler()) // the preferred port is taken
	defer busy.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(busy.URL, "http://"), ":")
	t.Setenv("SIGNET_PORT", port)
	logf, _ := os.CreateTemp(t.TempDir(), "log")
	t.Cleanup(func() { logf.Close() }) // Windows can't delete an open file
	go serve(dir, logf)
	secret, _ := loadSecret(dir) // racing serve to create it: both must end up with the same one
	var base string
	for i := 0; i < 100 && base == ""; i++ {
		base, _ = findEditor(dir, secret)
		time.Sleep(20 * time.Millisecond)
	}
	if base == "" || strings.HasSuffix(base, ":"+port) {
		t.Fatalf("editor not found on a free port: %q", base)
	}
}

func TestCLIGuardsAgainstMistakes(t *testing.T) {
	dir := testData(t)
	r := rand.New(rand.NewPCG(1, 2))
	tmp := t.TempDir()
	cases := map[string]string{
		`{"items":[{"type":"sign","page":1,"x":700,"y":10}]}`:            "off page 1",
		`{"items":[{"type":"text","page":1,"x":10,"y":-5,"text":"a"}]}`:  "off page 1",
		`{"items":[{"type":"text","page":1,"x":10,"y":10,"text":"日本"}]}`: "can't draw",
		`{"items":[{"type":"text","page":1,"x":10,"y":10,"size":900}]}`:  "between 1 and 200",
	}
	for js, want := range cases {
		var p Plan
		must(json.Unmarshal([]byte(js), &p))
		if _, err := apply(sample, p, filepath.Join(tmp, "x.pdf"), dir, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", js, err, want)
		}
	}
	src := filepath.Join(tmp, "Mine.pdf")
	b, _ := os.ReadFile(sample)
	os.WriteFile(src, b, 0o644)
	if _, err := apply(src, Plan{}, src, dir, r); err == nil || !strings.Contains(err.Error(), "overwrite the original") {
		t.Errorf("overwrote the original: %v", err)
	}
	var p Plan
	must(json.Unmarshal([]byte(`{"items":[{"type":"text","page":1,"x":72,"y":300,"text":"Paid 100%p in full, café, naïve – ok"}]}`), &p))
	if _, err := apply(sample, p, filepath.Join(tmp, "ok.pdf"), dir, r); err != nil {
		t.Errorf("Latin text with accents should work: %v", err)
	}
}

func TestLockedPDFs(t *testing.T) {
	locked := filepath.Join(t.TempDir(), "locked.pdf")
	c := conf()
	c.UserPW, c.OwnerPW = "s3cret", "s3cret"
	if err := api.EncryptFile(bg, sample, locked, c); err != nil {
		t.Fatal(err)
	}
	defer func() { pdfPassword = "" }()
	if _, err := inspect(locked, 1); err == nil || !strings.Contains(err.Error(), "--password") {
		t.Errorf("locked without password: %v", err)
	}
	pdfPassword = "wrong"
	if _, err := inspect(locked, 1); err == nil || !strings.Contains(err.Error(), "password does not open") {
		t.Errorf("wrong password: %v", err)
	}
	pdfPassword = "s3cret"
	if md, err := inspect(locked, 1); err != nil || !strings.Contains(md, "tenant.name") {
		t.Errorf("right password: %v", err)
	}
}

// Damaged and hostile PDFs must give an error, never a crash.
func TestBrokenPDFsNeverCrash(t *testing.T) {
	good, _ := os.ReadFile(sample)
	dir := testData(t)
	tmp := t.TempDir()
	r := rand.New(rand.NewPCG(3, 4))
	variants := [][]byte{
		[]byte("%PDF-1.7\n"), good[:len(good)/2], good[:len(good)-30],
		append(append([]byte{}, good[:200]...), bytes.Repeat([]byte("x"), 500)...),
	}
	for i := 0; i < 40; i++ { // random corruption
		b := append([]byte{}, good...)
		for j := 0; j < 1+r.IntN(20); j++ {
			b[9+r.IntN(len(b)-9)] = byte(r.IntN(256))
		}
		variants = append(variants, b)
	}
	var plan Plan
	must(json.Unmarshal([]byte(`{"fields":{"tenant.name":"x"},"items":[{"type":"sign","page":1,"x":100,"y":100}]}`), &plan))
	for i, v := range variants {
		f := filepath.Join(tmp, "b"+itoa(i)+".pdf")
		os.WriteFile(f, v, 0o644)
		inspect(f, 3)
		find(f, "Signature")
		apply(f, plan, filepath.Join(tmp, "o"+itoa(i)+".pdf"), dir, r)
	}
}

func FuzzInspect(f *testing.F) {
	good, _ := os.ReadFile(sample)
	f.Add(good)
	f.Add([]byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p := filepath.Join(t.TempDir(), "f.pdf")
		os.WriteFile(p, b, 0o644)
		inspect(p, 2)
		find(p, "a")
	})
}

// In a container the home folder may not exist or be read-only: the CLI must still work.
func TestWorksWithoutAHomeFolder(t *testing.T) {
	dir := testData(t)
	t.Setenv("HOME", "/nonexistent")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	var p Plan
	must(json.Unmarshal([]byte(`{"fields":{"tenant.name":"x"},"items":[{"type":"sign","page":1,"x":100,"y":100}]}`), &p))
	if _, err := apply(sample, p, filepath.Join(t.TempDir(), "o.pdf"), dir, rand.New(rand.NewPCG(1, 1))); err != nil {
		t.Fatal(err)
	}
}
