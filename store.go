package main

// Your profile and signature bank, kept as two JSON files in one folder
// (~/.signet, or SIGNET_DATA). The editor writes them; the CLI reads them.
//
// Writes are crash-safe (temp file, fsync, rename) and every change keeps the
// previous version in backups/ (the last 10 per file). If a file is ever
// damaged, the newest good backup is used instead and put back.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const keepBackups = 10

var storeMu sync.Mutex // one writer at a time, even with several editor windows

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
// A damaged file is replaced by its newest good backup.
func readStore(dir, key string) ([]byte, error) {
	b, err := os.ReadFile(storePath(dir, key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Valid(b) {
		return b, nil
	}
	good, from := newestGoodBackup(dir, key)
	if good == nil {
		return nil, fmt.Errorf("%s is damaged and there is no backup to restore", storePath(dir, key))
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	damaged := storePath(dir, key) + ".damaged-" + time.Now().Format("20060102-150405")
	os.Rename(storePath(dir, key), damaged)
	if err := writeAtomic(storePath(dir, key), good, 0o600); err != nil {
		return nil, err
	}
	log.Printf("%s was damaged (kept as %s); restored from %s", key, damaged, from)
	return good, nil
}

func writeStore(dir, key string, body []byte) error {
	if !json.Valid(body) {
		return fmt.Errorf("not json")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	path := storePath(dir, key)
	if old, err := os.ReadFile(path); err == nil && json.Valid(old) && string(old) != string(body) {
		backup(dir, key, old)
	}
	return writeAtomic(path, body, 0o600)
}

func backupDir(dir string) string { return filepath.Join(dir, "backups") }

func backup(dir, key string, data []byte) {
	bd := backupDir(dir)
	if os.MkdirAll(bd, 0o700) != nil {
		return
	}
	name := fmt.Sprintf("%s-%s.json", key, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := writeAtomic(filepath.Join(bd, name), data, 0o600); err != nil {
		log.Printf("backup of %s failed: %v", key, err)
		return
	}
	old := backups(dir, key)
	for len(old) > keepBackups {
		os.Remove(filepath.Join(bd, old[0]))
		old = old[1:]
	}
}

// backups lists the backup file names for key, oldest first.
func backups(dir, key string) []string {
	entries, _ := os.ReadDir(backupDir(dir))
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), key+"-") && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func newestGoodBackup(dir, key string) ([]byte, string) {
	list := backups(dir, key)
	for i := len(list) - 1; i >= 0; i-- {
		p := filepath.Join(backupDir(dir), list[i])
		if b, err := os.ReadFile(p); err == nil && json.Valid(b) {
			return b, p
		}
	}
	return nil, ""
}

// writeAtomic: write to a temp file in the same folder, flush it to disk, then
// rename it over the target, so a crash leaves either the old file or the new one.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(filepath.Dir(path)); err == nil { // make the rename itself durable
		d.Sync()
		d.Close()
	}
	return nil
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
