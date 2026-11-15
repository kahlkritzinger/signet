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
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	must(writeStore(dir, "bank", bank))
	must(writeStore(dir, "profile", []byte(prof)))
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

func TestServer(t *testing.T) {
	dir := t.TempDir()
	h := newServer(dir)
	do := func(method, target, host, origin string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, bytes.NewReader(body))
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const local = "127.0.0.1:7766"
	if w := do("GET", "/", local, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Signet</title>") {
		t.Errorf("page: %d", w.Code)
	}
	if w := do("GET", "/api/store/bank", "evil.example:7766", "", nil); w.Code != 403 {
		t.Errorf("DNS rebinding not blocked: %d", w.Code)
	}
	if w := do("PUT", "/api/store/bank", local, "https://evil.example", []byte("[]")); w.Code != 403 {
		t.Errorf("foreign origin not blocked: %d", w.Code)
	}
	if w := do("GET", "/api/store/bank", local, "", nil); w.Body.String() != "null" {
		t.Errorf("empty bank: %q", w.Body.String())
	}
	if w := do("PUT", "/api/store/bank", local, "http://"+local, []byte(`[{"id":"a"}]`)); w.Code != 200 {
		t.Errorf("save bank: %d", w.Code)
	}
	if w := do("GET", "/api/store/bank", local, "", nil); w.Body.String() != `[{"id":"a"}]` {
		t.Errorf("bank round trip: %q", w.Body.String())
	}
	if w := do("PUT", "/api/store/profile", local, "", []byte("not json")); w.Code != 400 {
		t.Errorf("bad json: %d", w.Code)
	}
	if w := do("GET", "/api/store/secrets", local, "", nil); w.Code != 404 {
		t.Errorf("unknown key: %d", w.Code)
	}

	pdf, _ := os.ReadFile(sample)
	orig := filepath.Join(t.TempDir(), "Lease.pdf")
	os.WriteFile(orig, pdf, 0o644)
	up := func(path string) string {
		w := do("POST", "/api/docs?name=Lease.pdf&path="+path, local, "", pdf)
		var r struct{ ID string }
		json.Unmarshal(w.Body.Bytes(), &r)
		return r.ID
	}
	id := up(orig)
	if w := do("GET", "/api/doc/"+id, local, "", nil); w.Code != 200 || w.Header().Get("X-Name") != "Lease.pdf" {
		t.Errorf("get doc: %d %q", w.Code, w.Header().Get("X-Name"))
	}
	if w := do("POST", "/api/docs", local, "", []byte("hello")); w.Code != 400 {
		t.Errorf("non-PDF accepted: %d", w.Code)
	}
	save := func(id string, body []byte) (int, string) {
		w := do("POST", "/api/save/"+id, local, "", body)
		var r struct{ Path string }
		json.Unmarshal(w.Body.Bytes(), &r)
		return w.Code, r.Path
	}
	_, first := save(id, pdf)
	if first != filepath.Join(filepath.Dir(orig), "Lease-signed.pdf") {
		t.Errorf("first save went to %q", first)
	}
	if _, again := save(id, pdf); again != first {
		t.Errorf("saving again should overwrite our own file, went to %q", again)
	}
	if _, other := save(up(orig), pdf); !strings.HasSuffix(other, "Lease-signed-2.pdf") {
		t.Errorf("a second document must not overwrite the first's file: %q", other)
	}
	if code, _ := save(id, []byte("nope")); code != 400 {
		t.Errorf("non-PDF saved: %d", code)
	}
	if code, _ := save(up(""), pdf); code != 404 {
		t.Errorf("doc without original saved: %d", code)
	}
	if code, _ := save("abc", pdf); code != 404 {
		t.Errorf("bad id: %d", code)
	}
	_ = http.StatusOK
}
