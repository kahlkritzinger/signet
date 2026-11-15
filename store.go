package main

// Your profile and signature bank, kept as two JSON files in one folder
// (~/.signet, or SIGNET_DATA). The editor writes them; the CLI reads them.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mark is one signature or set of initials: a transparent PNG (as a data URL)
// whose alpha is the ink, tinted at use. Kind is "sig" or "ini"; Ar = width / height.
type Mark struct {
	ID   string  `json:"id"`
	Kind string  `json:"kind"`
	Mask string  `json:"mask"`
	Ar   float64 `json:"ar"`
}

type Field struct {
	Key   string `json:"key,omitempty"`
	Label string `json:"label"`
	Value string `json:"value"`
	Addr  bool   `json:"addr,omitempty"`
}

type Group struct {
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

type Profile struct {
	Groups []Group `json:"groups"`
}

var storeKeys = map[string]bool{"bank": true, "profile": true}

func dataDir() string {
	if d := os.Getenv("SIGNET_DATA"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".signet"
	}
	return filepath.Join(home, ".signet")
}

func storePath(dir, key string) string { return filepath.Join(dir, key+".json") }

// readStore returns the raw JSON for key, or nil if it has never been saved.
func readStore(dir, key string) ([]byte, error) {
	b, err := os.ReadFile(storePath(dir, key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func writeStore(dir, key string, body []byte) error {
	if !json.Valid(body) {
		return fmt.Errorf("not json")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(storePath(dir, key), body, 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadBank(dir string) ([]Mark, error) {
	b, err := readStore(dir, "bank")
	if err != nil || b == nil {
		return nil, err
	}
	var all []Mark
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("bank.json: %w", err)
	}
	var ok []Mark
	for _, m := range all {
		if (m.Kind == "sig" || m.Kind == "ini") && m.Mask != "" {
			ok = append(ok, m)
		}
	}
	return ok, nil
}

func loadProfile(dir string) (Profile, error) {
	var p Profile
	b, err := readStore(dir, "profile")
	if err != nil || b == nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("profile.json: %w", err)
	}
	return p, nil
}

// Entry is one thing the profile can fill in, like the editor's Profile menu.
type Entry struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// profileEntries composes the profile into label -> value, in menu order:
// full name, initials and surname, the plain details, the address, then other groups.
func profileEntries(p Profile) []Entry {
	var out []Entry
	add := func(l, v string) {
		if strings.TrimSpace(l) != "" && strings.TrimSpace(v) != "" {
			out = append(out, Entry{strings.TrimSpace(l), strings.TrimSpace(v)})
		}
	}
	if len(p.Groups) == 0 {
		return out
	}
	g0 := p.Groups[0].Fields
	v := func(k string) string {
		for _, f := range g0 {
			if f.Key == k {
				return strings.TrimSpace(f.Value)
			}
		}
		return ""
	}
	if v("first") != "" && v("sur") != "" {
		add("Full name", v("first")+" "+v("sur"))
		if v("second") != "" {
			add("Full names", v("first")+" "+v("second")+" "+v("sur"))
		}
		ini := ""
		for _, s := range []string{v("first"), v("second")} {
			if s != "" {
				ini += strings.ToUpper(string([]rune(s)[0]))
			}
		}
		add("Initials & surname", ini+" "+v("sur"))
	}
	for _, f := range g0 {
		if !f.Addr {
			add(f.Label, f.Value)
		}
	}
	street := join(" ", v("streetnr"), v("street"))
	local := nonEmpty(v("unit"), street, v("suburb"), v("city"), v("postal"))
	full := append(append([]string{}, local...), nonEmpty(v("province"), v("country"))...)
	if len(full) > 0 {
		add("Address", strings.Join(full, ", "))
		add("Address (lines)", strings.Join(full, "\n"))
		add("Street address", street)
		for _, f := range g0 {
			if f.Addr {
				add(f.Label, f.Value)
			}
		}
	}
	for _, g := range p.Groups[1:] {
		name := g.Name
		if name == "" {
			name = "Group"
		}
		for _, f := range g.Fields {
			if strings.TrimSpace(f.Label) != "" {
				add(name+": "+f.Label, f.Value)
			}
		}
	}
	return out
}

func lookup(entries []Entry, label string) (string, error) {
	for _, e := range entries {
		if strings.EqualFold(e.Label, label) {
			return e.Value, nil
		}
	}
	var have []string
	for _, e := range entries {
		have = append(have, e.Label)
	}
	if len(have) == 0 {
		return "", fmt.Errorf("your profile is empty: run `signet` and fill it in (Profile, or press P)")
	}
	return "", fmt.Errorf("profile has no %q. It has: %s", label, strings.Join(have, ", "))
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func join(sep string, xs ...string) string { return strings.Join(nonEmpty(xs...), sep) }
