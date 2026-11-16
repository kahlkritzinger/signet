package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cloud stands in for Cloudflare in front of a Signet server: it lets in the
// service token's ID and secret (adding a signed Access token, as Cloudflare
// does) and turns everyone else away with a login page.
func cloud(t *testing.T, f *fakeAccess, serverDir string) *httptest.Server {
	t.Setenv("SIGNET_ACCESS_SYNC_AUD", "sync-aud")
	t.Setenv("SIGNET_ACCESS_SYNC_CLIENT", "mac.access")
	h, err := newServer(serverDir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("CF-Access-Client-Id") != "mac.access" || r.Header.Get("CF-Access-Client-Secret") != "s3cret" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(403)
			w.Write([]byte("<html>Forbidden</html>"))
			return
		}
		now := time.Now().Unix()
		r.Header.Set("Cf-Access-Jwt-Assertion", f.token(map[string]any{"iss": "https://" + f.team, "aud": []string{"sync-aud"},
			"exp": now + 3600, "iat": now, "common_name": "mac.access"}, f.key))
		r.Host = "signet.example.com"
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestOneAccountAcrossMachines(t *testing.T) {
	f := newFakeAccess(t)
	server := testData(t)
	ts := cloud(t, f, server)
	mac := t.TempDir()
	writeLocal(mac, "bank", []byte(`[]`), "")

	if err := connectAccount(mac, ts.URL, "mac.access", "wrong"); err == nil || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := os.Stat(accountPath(mac)); err == nil {
		t.Fatal("a failed connect was remembered")
	}
	if err := connectAccount(mac, "http://signet.example.com", "mac.access", "s3cret"); err == nil {
		t.Error("connected over plain http to another machine")
	}
	if err := connectAccount(mac, ts.URL, "mac.access", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(accountPath(mac)); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("account file is %v", st.Mode().Perm())
	}

	// The server's signatures win; the Mac's old copy goes to its backups.
	want, _ := readLocal(server, "bank")
	if got, err := readStore(mac, "bank"); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("bank on the Mac: %v %.40s", err, got)
	}
	if len(backups(mac, "bank")) == 0 {
		t.Error("the Mac's old bank was not kept")
	}

	// A change on the Mac lands on the server, and is guarded by its version.
	cur, _ := readStore(mac, "profile")
	v1 := storeVersion(cur)
	p2 := []byte(`{"groups":[{"name":"Me","fields":[{"label":"First name","value":"Kim"}]}]}`)
	v2, err := writeStore(mac, "profile", p2, v1)
	if err != nil || v2 != storeVersion(p2) {
		t.Fatalf("save from the Mac: %q %v", v2, err)
	}
	if got, _ := readLocal(server, "profile"); !bytes.Equal(got, p2) {
		t.Fatalf("server has %s", got)
	}
	// Changed on the phone in the meantime: the Mac's stale save is refused, not lost silently.
	writeLocal(server, "profile", []byte(`{"groups":[]}`), "")
	if _, err := writeStore(mac, "profile", p2, v1); !errors.Is(err, errChanged) {
		t.Fatalf("stale save: %v", err)
	}

	// Offline: reading uses the copy on the Mac; saving refuses rather than drift apart.
	ts.Close()
	if got, err := readStore(mac, "bank"); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("offline read: %v", err)
	}
	if _, err := writeStore(mac, "bank", []byte(`[]`), ""); err == nil || !strings.Contains(err.Error(), "NOT saved") {
		t.Fatalf("offline save: %v", err)
	}
	if err := accountCmd(mac, []string{"disconnect"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readStore(mac, "bank"); !bytes.Equal(got, want) {
		t.Error("disconnecting lost the Mac's copy")
	}
}

func TestConnectUploadsWhatOnlyThisMachineHas(t *testing.T) {
	f := newFakeAccess(t)
	server := t.TempDir()
	ts := cloud(t, f, server)
	mac := testData(t)
	if err := connectAccount(mac, ts.URL, "mac.access", "s3cret"); err != nil {
		t.Fatal(err)
	}
	a, _ := readLocal(mac, "profile")
	if b, _ := readLocal(server, "profile"); !bytes.Equal(a, b) {
		t.Fatal("profile was not uploaded")
	}
}

func TestSyncNeedsTheRightToken(t *testing.T) {
	f := newFakeAccess(t)
	t.Setenv("SIGNET_ACCESS_SYNC_AUD", "sync-aud")
	t.Setenv("SIGNET_ACCESS_SYNC_CLIENT", "mac.access")
	c := newClient(t, testData(t))
	now := time.Now().Unix()
	tok := func(aud, cn, email string) string {
		return f.token(map[string]any{"iss": "https://" + f.team, "aud": []string{aud}, "exp": now + 3600,
			"common_name": cn, "email": email}, f.key)
	}
	cases := []struct {
		name, path, token string
		want              int
	}{
		{"this machine", "/api/sync/bank", tok("sync-aud", "mac.access", ""), 200},
		{"a token you didn't allow", "/api/sync/bank", tok("sync-aud", "other.access", ""), 401},
		{"your browser login can't sync", "/api/sync/bank", tok("aud-1", "", "me@example.com"), 401},
		{"the sync token can't open the editor's data", "/api/store/bank", tok("sync-aud", "mac.access", ""), 401},
		{"nothing", "/api/sync/bank", "", 401},
	}
	for _, tc := range cases {
		w := c.do("GET", tc.path, "", nil, "Host", "signet.example.com", "Cf-Access-Jwt-Assertion", tc.token)
		if w.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, w.Code, tc.want)
		}
	}
	t.Setenv("SIGNET_ACCESS_SYNC_CLIENT", "")
	if _, err := accessFromEnv(); err == nil {
		t.Error("a sync audience without allowed tokens was accepted")
	}
}

func TestTwoWindowsNeverOverwriteEachOther(t *testing.T) {
	c := newClient(t, testData(t))
	w := c.do("GET", "/api/store/profile", "cli", nil)
	v := w.Header().Get(versionHeader)
	if v == "" {
		t.Fatal("no version")
	}
	w = c.do("PUT", "/api/store/profile", "cli", []byte(`{"groups":[]}`), ifVersionHeader, v)
	if w.Code != 200 || w.Header().Get(versionHeader) == v {
		t.Fatalf("first save: %d", w.Code)
	}
	w = c.do("PUT", "/api/store/profile", "cli", []byte(`{"groups":[{"name":"x","fields":[]}]}`), ifVersionHeader, v)
	if w.Code != 409 {
		t.Fatalf("second window's stale save: %d", w.Code)
	}
}
