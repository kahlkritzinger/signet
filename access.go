package main

// Running Signet on a server, behind Cloudflare Access.
//
// Signet normally runs on your own machine. To reach it from your phone you can
// run `signet serve` on a server behind Cloudflare Access (Zero Trust). Access
// signs in the person and adds a signed token (JWT) to every request; Signet
// checks that token (signature, audience, issuer, expiry) and the email in it.
//
//	SIGNET_ACCESS_TEAM   your team domain, e.g. myteam.cloudflareaccess.com
//	SIGNET_ACCESS_AUD    the Access application's Audience (AUD) tag
//	SIGNET_ACCESS_EMAIL  who may use it (comma-separated)
//	SIGNET_HOSTS         the public hostname(s), e.g. signet.example.com
//
// Without these, Signet refuses to listen anywhere but 127.0.0.1.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// certsURL is where the team publishes its signing keys (a variable so tests can point it elsewhere).
var certsURL = func(team string) string { return "https://" + team + "/cdn-cgi/access/certs" }

type accessAuth struct {
	team, aud string
	emails    map[string]bool
	certsURL  string
	client    *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// accessFromEnv returns nil when Signet isn't set up behind Cloudflare Access.
func accessFromEnv() (*accessAuth, error) {
	team := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(os.Getenv("SIGNET_ACCESS_TEAM")), "https://"), "/")
	if team == "" {
		return nil, nil
	}
	aud := strings.TrimSpace(os.Getenv("SIGNET_ACCESS_AUD"))
	emails := map[string]bool{}
	for _, e := range strings.Split(os.Getenv("SIGNET_ACCESS_EMAIL"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			emails[e] = true
		}
	}
	if aud == "" || len(emails) == 0 {
		return nil, fmt.Errorf("SIGNET_ACCESS_TEAM is set, so SIGNET_ACCESS_AUD and SIGNET_ACCESS_EMAIL are needed too")
	}
	return &accessAuth{team: team, aud: aud, emails: emails, certsURL: certsURL(team),
		client: &http.Client{Timeout: 10 * time.Second}}, nil
}

// email returns the signed-in, allowed email for this request, or "".
func (a *accessAuth) email(r *http.Request) string {
	tok := r.Header.Get("Cf-Access-Jwt-Assertion")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	var hdr struct{ Alg, Kid string }
	if !decodePart(parts[0], &hdr) || hdr.Alg != "RS256" {
		return ""
	}
	key := a.key(hdr.Kid)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if key == nil || err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return ""
	}
	var c struct {
		Iss   string          `json:"iss"`
		Aud   json.RawMessage `json:"aud"`
		Exp   float64         `json:"exp"`
		Nbf   float64         `json:"nbf"`
		Email string          `json:"email"`
	}
	if !decodePart(parts[1], &c) {
		return ""
	}
	now := float64(time.Now().Unix())
	if c.Iss != "https://"+a.team || !hasAud(c.Aud, a.aud) || c.Exp == 0 || now > c.Exp+30 || (c.Nbf != 0 && now < c.Nbf-30) {
		return ""
	}
	if e := strings.ToLower(c.Email); a.emails[e] {
		return e
	}
	return ""
}

func decodePart(s string, v any) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && json.Unmarshal(b, v) == nil
}

func hasAud(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

// key finds the signing key, refreshing the team's keys hourly or when a new one appears.
func (a *accessAuth) key(kid string) *rsa.PublicKey {
	a.mu.Lock()
	defer a.mu.Unlock()
	if k := a.keys[kid]; k != nil && time.Since(a.fetched) < time.Hour {
		return k
	}
	if time.Since(a.fetched) < 10*time.Second && a.keys != nil { // don't hammer Cloudflare with made-up key ids
		return a.keys[kid]
	}
	a.fetched = time.Now()
	resp, err := a.client.Get(a.certsURL)
	if err != nil {
		return a.keys[kid]
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct{ Kid, Kty, N, E string } `json:"keys"`
	}
	if json.NewDecoder(resp.Body).Decode(&set) != nil {
		return a.keys[kid]
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) > 0 {
		a.keys = keys
	}
	return a.keys[kid]
}
