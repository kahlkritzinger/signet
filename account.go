package main

// One account on all your machines.
//
// Run Signet on a server behind Cloudflare Access (see access.go), then on each
// of your machines:
//
//	SIGNET_ACCOUNT_ID=... SIGNET_ACCOUNT_SECRET=... signet account connect https://signet.example.com
//
// From then on the editor and the CLI on that machine read and save your
// signatures and profile on the server, so a signature drawn on your phone is
// on your laptop too, and the other way round. PDFs never leave the machine.
//
// The ID and secret are a Cloudflare Access service token, kept in
// ~/.signet/account.json (readable only by you). A copy of your data stays on
// the machine: when the server can't be reached Signet reads that copy, and
// refuses to save until the server is back, so the two never drift apart.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type account struct {
	URL          string `json:"url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func accountPath(dir string) string { return filepath.Join(dir, "account.json") }

// loadAccount returns nil when this machine keeps its own data.
func loadAccount(dir string) (*account, error) {
	b, err := os.ReadFile(accountPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a account
	if json.Unmarshal(b, &a) != nil || a.URL == "" {
		return nil, fmt.Errorf("%s is damaged: run signet account connect again", accountPath(dir))
	}
	return &a, nil
}

var accountHTTP = &http.Client{Timeout: 20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// offline means the server could not be reached at all (no network, server down).
type offline struct{ err error }

func (o offline) Error() string { return o.err.Error() }

// do calls the server's sync route; it returns the body and its version.
func (a *account) do(method, key string, body []byte, ifVersion string) ([]byte, string, error) {
	req, err := http.NewRequest(method, strings.TrimSuffix(a.URL, "/")+"/api/sync/"+key, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("CF-Access-Client-Id", a.ClientID)
	req.Header.Set("CF-Access-Client-Secret", a.ClientSecret)
	req.Header.Set("User-Agent", "signet/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifVersion != "" {
		req.Header.Set(ifVersionHeader, ifVersion)
	}
	resp, err := accountHTTP.Do(req)
	if err != nil {
		var ne net.Error
		var ue *url.Error
		if errors.As(err, &ne) || errors.As(err, &ue) {
			return nil, "", offline{err}
		}
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxStore+1))
	if err != nil {
		return nil, "", offline{err}
	}
	isJSON := strings.Contains(resp.Header.Get("Content-Type"), "json")
	switch {
	case resp.StatusCode == 200 && isJSON && json.Valid(b):
		return b, resp.Header.Get(versionHeader), nil
	case resp.StatusCode == 409:
		return nil, "", errChanged
	case resp.StatusCode == 401 || resp.StatusCode == 403 || (resp.StatusCode >= 300 && resp.StatusCode < 400):
		return nil, "", fmt.Errorf("%s did not accept this machine: its account token may be revoked or expired (run signet account connect again)", a.URL)
	case resp.StatusCode >= 500 && !isJSON:
		return nil, "", offline{fmt.Errorf("%s answered %s", a.URL, resp.Status)}
	default:
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, "", fmt.Errorf("%s: %s", a.URL, e.Error)
	}
}

func (a *account) read(dir, key string) ([]byte, error) {
	b, _, err := a.do("GET", key, nil, "")
	var off offline
	if errors.As(err, &off) {
		log.Printf("can't reach %s (%v); using the copy of your %s on this machine", a.URL, err, key)
		return readLocal(dir, key)
	}
	if err != nil {
		return nil, err
	}
	if string(bytes.TrimSpace(b)) == "null" {
		return nil, nil
	}
	if old, _ := os.ReadFile(storePath(dir, key)); !bytes.Equal(old, b) {
		if _, err := writeLocal(dir, key, b, ""); err != nil {
			log.Printf("could not keep a copy of your %s on this machine: %v", key, err)
		}
	}
	return b, nil
}

func (a *account) write(dir, key string, body []byte, ifVersion string) (string, error) {
	_, v, err := a.do("PUT", key, body, ifVersion)
	var off offline
	if errors.As(err, &off) {
		return "", fmt.Errorf("can't reach %s, so this change was NOT saved; try again when you're online (%v)", a.URL, err)
	}
	if err != nil {
		return "", err
	}
	if _, err := writeLocal(dir, key, body, ""); err != nil {
		log.Printf("saved on %s, but could not keep a copy on this machine: %v", a.URL, err)
	}
	if v == "" {
		v = storeVersion(body)
	}
	return v, nil
}

func accountCmd(dir string, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		a, err := loadAccount(dir)
		if err != nil {
			return err
		}
		if a == nil {
			fmt.Printf("No account: your signatures and profile are kept on this machine only (%s).\n", dir)
			return nil
		}
		if _, _, err := a.do("GET", "profile", nil, ""); err != nil {
			return fmt.Errorf("account %s: %v", a.URL, err)
		}
		fmt.Printf("Connected to %s: your signatures and profile are kept there (a copy stays in %s).\n", a.URL, dir)
		return nil
	case "connect":
		if len(args) != 2 {
			return fmt.Errorf("usage: SIGNET_ACCOUNT_ID=... SIGNET_ACCOUNT_SECRET=... signet account connect https://your-signet-server")
		}
		return connectAccount(dir, args[1], strings.TrimSpace(os.Getenv("SIGNET_ACCOUNT_ID")), strings.TrimSpace(os.Getenv("SIGNET_ACCOUNT_SECRET")))
	case "disconnect":
		if err := os.Remove(accountPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("Disconnected. This machine keeps your signatures and profile as they were last synced.")
		return nil
	}
	return fmt.Errorf("usage: signet account [connect URL | disconnect]")
}

// connectAccount checks the server answers, brings this machine's data in line
// with it (the server wins; this machine's old copy goes to its backups; data
// only this machine has is uploaded), then remembers the account.
func connectAccount(dir, raw, id, secret string) error {
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && localHost(u.Host))) {
		return fmt.Errorf("the account address must start with https://")
	}
	if id == "" || secret == "" {
		return fmt.Errorf("set SIGNET_ACCOUNT_ID and SIGNET_ACCOUNT_SECRET (the service token for this machine) first")
	}
	a := &account{URL: u.String(), ClientID: id, ClientSecret: secret}
	for _, key := range []string{"bank", "profile"} {
		remote, v, err := a.do("GET", key, nil, "")
		if err != nil {
			return fmt.Errorf("could not connect: %v", err)
		}
		local, err := readLocal(dir, key)
		if err != nil {
			return err
		}
		switch {
		case string(bytes.TrimSpace(remote)) == "null" && local != nil:
			if _, _, err := a.do("PUT", key, local, v); err != nil {
				return fmt.Errorf("could not upload your %s: %v", key, err)
			}
			fmt.Printf("Uploaded this machine's %s to %s.\n", key, a.URL)
		case string(bytes.TrimSpace(remote)) != "null" && !bytes.Equal(remote, local):
			if _, err := writeLocal(dir, key, remote, ""); err != nil {
				return err
			}
			if local != nil {
				fmt.Printf("Your %s now comes from %s (this machine's old copy is in %s).\n", key, a.URL, backupDir(dir))
			}
		}
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(accountPath(dir), b, 0o600); err != nil {
		return err
	}
	fmt.Printf("Connected to %s. Signatures and profile are now shared with every machine connected to it.\n", a.URL)
	return nil
}
