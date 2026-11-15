package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAccess is a stand-in for Cloudflare Access: it publishes a key and signs tokens.
type fakeAccess struct {
	key  *rsa.PrivateKey
	team string
}

func newFakeAccess(t *testing.T) *fakeAccess {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeAccess{key: key, team: "team.example.cloudflareaccess.com"}
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(certs.Close)
	old := certsURL
	certsURL = func(string) string { return certs.URL }
	t.Cleanup(func() { certsURL = old })
	t.Setenv("SIGNET_ACCESS_TEAM", f.team)
	t.Setenv("SIGNET_ACCESS_AUD", "aud-1")
	t.Setenv("SIGNET_ACCESS_EMAIL", "me@example.com")
	t.Setenv("SIGNET_HOSTS", "signet.example.com")
	return f
}

func (f *fakeAccess) token(claims map[string]any, key *rsa.PrivateKey) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	head := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(head))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return head + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestBehindCloudflareAccessOnlyYouGetIn(t *testing.T) {
	f := newFakeAccess(t)
	c := newClient(t, t.TempDir())
	now := time.Now().Unix()
	good := map[string]any{"iss": "https://" + f.team, "aud": []string{"aud-1"}, "exp": now + 3600, "iat": now, "email": "Me@Example.com"}
	with := func(change map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range change {
			m[k] = v
		}
		return m
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := []struct {
		name, host, token string
		want              int
	}{
		{"your login", "signet.example.com", f.token(good, f.key), 200},
		{"someone else", "signet.example.com", f.token(with(map[string]any{"email": "eve@example.com"}), f.key), 401},
		{"another app's token", "signet.example.com", f.token(with(map[string]any{"aud": "aud-2"}), f.key), 401},
		{"another team", "signet.example.com", f.token(with(map[string]any{"iss": "https://evil.cloudflareaccess.com"}), f.key), 401},
		{"expired", "signet.example.com", f.token(with(map[string]any{"exp": now - 3600}), f.key), 401},
		{"forged signature", "signet.example.com", f.token(good, other), 401},
		{"no token", "signet.example.com", "", 401},
		{"garbage", "signet.example.com", "a.b.c", 401},
		{"unknown hostname", "evil.example.com", f.token(good, f.key), 403},
	}
	for _, tc := range cases {
		w := c.do("GET", "/api/store/bank", "", nil, "Host", tc.host, "Cf-Access-Jwt-Assertion", tc.token)
		if w.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, w.Code, tc.want)
		}
	}
	if w := c.do("PUT", "/api/store/profile", "", []byte(`{"groups":[]}`), "Host", "signet.example.com",
		"Cf-Access-Jwt-Assertion", f.token(good, f.key), "Origin", "https://signet.example.com"); w.Code != 200 {
		t.Errorf("saving from the hosted page: %d", w.Code)
	}
}

func TestNeverExposedWithoutALogin(t *testing.T) {
	t.Setenv("SIGNET_HOSTS", "signet.example.com")
	if _, err := newServer(t.TempDir()); err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("extra hostnames without a login: %v", err)
	}
	t.Setenv("SIGNET_HOSTS", "")
	t.Setenv("SIGNET_LISTEN", "0.0.0.0:0")
	if err := serve(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "without a login") {
		t.Errorf("listening on all interfaces without a login: %v", err)
	}
	t.Setenv("SIGNET_ACCESS_TEAM", "team.example.cloudflareaccess.com")
	if _, err := accessFromEnv(); err == nil {
		t.Error("a team without audience and email was accepted")
	}
}
