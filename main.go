// Signet: fill, sign and initial PDFs. An editor on your own machine, and a
// CLI for you and your AI. Your signatures and profile never leave your computer.
package main

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var version = "dev" // set by the release build

const usage = `Signet: fill, sign and initial PDFs. Your signatures stay on your machine.

  signet [open] [FILE.pdf ...]       the editor, in its own window
  signet set-default                 make Signet the app that opens PDFs
  signet inspect FILE [--pages 5]    Markdown: pages, form fields (name, x, y), text
  signet find FILE "Signature:"      where a phrase is (page, x, y), to place things by it
  signet profile                     what can be filled in: "Full name", "ID number", ...
  signet bank                        your signatures and initials
  signet apply FILE PLAN.json [--out OUT]   fill, write, sign, initial -> FILE-signed.pdf
  signet serve                       run the editor in this terminal (it normally runs in the background)
  signet version

  Locked PDF? Add --password X (or set SIGNET_PDF_PASSWORD) to inspect, find or apply.

Coordinates are PDF points from the TOP-LEFT of the page. PLAN.json:

  {"fields": {"tenant.name": {"profile": "Full name"}, "agree": true, "term": "12 months"},
   "items": [
     {"type": "sign",    "page": 3, "x": 130, "y": 700},
     {"type": "date",    "page": 3, "x": 330, "y": 702, "format": "long"},
     {"type": "text",    "page": 1, "x": 72,  "y": 300, "profile": "ID number"},
     {"type": "text",    "page": 1, "x": 72,  "y": 320, "text": "Cape Town", "size": 11},
     {"type": "initial", "page": "every", "x": 500, "y": 780},
     {"type": "check",   "page": 2, "x": 60,  "y": 410}]}

  types: text, date, sign, initial, check, cross   page: number or "every"
  ink: black, blue, red (signatures default blue)   width: signature width in points
  date formats: long (2 January 2026), short, iso, dmy

Your profile and bank live in ~/.signet (SIGNET_DATA). Set them up once in the
editor; the CLI reads them. The editor listens on 127.0.0.1:7766 (SIGNET_PORT)
and stops by itself 10 minutes after its last window closes. Only you can reach it:
the CLI proves itself with ~/.signet/secret, the window with a one-time code.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "signet:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "open"
	if len(args) > 0 && !strings.HasSuffix(strings.ToLower(args[0]), ".pdf") {
		cmd, args = args[0], args[1:]
	}
	dir := dataDir()
	args = takePassword(args)
	switch cmd {
	case "inspect", "find", "apply": // a pathological PDF must never hang an AI's terminal forever
		limit := 2 * time.Minute
		if s, err := strconv.Atoi(os.Getenv("SIGNET_TIMEOUT")); err == nil && s > 0 {
			limit = time.Duration(s) * time.Second
		}
		time.AfterFunc(limit, func() {
			fmt.Fprintf(os.Stderr, "signet: gave up after %s; this PDF is too large or unusual (SIGNET_TIMEOUT=seconds to wait longer)\n", limit)
			os.Exit(2)
		})
	}
	switch cmd {
	case "open":
		return openEditor(args, dir)
	case "serve":
		return serve(dir, os.Stderr)
	case "set-default":
		return setDefault()
	case "inspect":
		fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
		pages := fs.Int("pages", 5, "pages of text to show")
		file, err := oneFile(fs, args)
		if err != nil {
			return err
		}
		md, err := inspect(file, *pages)
		if err == nil {
			fmt.Print(md)
		}
		return err
	case "find":
		if len(args) != 2 {
			return fmt.Errorf(`usage: signet find FILE "text"`)
		}
		hits, err := find(args[0], args[1])
		if err != nil {
			return err
		}
		if hits == nil {
			hits = []Hit{}
		}
		return printJSON(hits)
	case "profile":
		p, err := loadProfile(dir)
		if err != nil {
			return err
		}
		e := profileEntries(p)
		if e == nil {
			e = []Entry{}
		}
		return printJSON(e)
	case "bank":
		bank, err := loadBank(dir)
		if err != nil {
			return err
		}
		type row struct {
			ID     string  `json:"id"`
			Kind   string  `json:"kind"`
			Aspect float64 `json:"aspect"`
		}
		rows := []row{}
		for _, m := range bank {
			rows = append(rows, row{m.ID, map[string]string{"sig": "signature", "ini": "initials"}[m.Kind], round1(m.Ar)})
		}
		return printJSON(rows)
	case "apply":
		fs := flag.NewFlagSet("apply", flag.ContinueOnError)
		out := fs.String("out", "", "output PDF (default FILE-signed.pdf)")
		var pos []string
		for len(args) > 0 { // allow flags after the file names
			if err := fs.Parse(args); err != nil {
				return err
			}
			if fs.NArg() == 0 {
				break
			}
			pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
		}
		if len(pos) != 2 {
			return fmt.Errorf("usage: signet apply FILE PLAN.json [--out OUT]   (PLAN - reads stdin)")
		}
		var raw []byte
		var err error
		if pos[1] == "-" {
			raw, err = readAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(pos[1])
		}
		if err != nil {
			return err
		}
		var plan Plan
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&plan); err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		res, err := apply(pos[0], plan, *out, dir, rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7)))
		if err != nil {
			return err
		}
		return printJSON(res)
	case "version", "--version":
		fmt.Println("signet", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (signet help)", cmd)
}

// takePassword pulls --password X (or --password=X) out of the arguments.
func takePassword(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--password" || a == "-password":
			if i+1 < len(args) {
				pdfPassword = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--password="):
			pdfPassword = strings.TrimPrefix(a, "--password=")
		default:
			out = append(out, a)
		}
	}
	return out
}

func oneFile(fs *flag.FlagSet, args []string) (string, error) {
	var file string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		if file != "" {
			return "", fmt.Errorf("one file at a time")
		}
		file, args = fs.Arg(0), fs.Args()[1:]
	}
	if file == "" {
		return "", fmt.Errorf("which PDF?")
	}
	return file, nil
}

func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", " ")
	e.SetEscapeHTML(false)
	return e.Encode(v)
}

func readAll(f *os.File) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(f)
	return b.Bytes(), err
}

func itoa(n int) string { return strconv.Itoa(n) }

// ---------- the editor ----------

// editorFile records where this user's editor is listening, so a busy port
// (another program, or another account's Signet) never gets in the way.
type editorInfo struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

func editorPath(dir string) string { return filepath.Join(dir, "editor.json") }

func preferredPort() string {
	if p := os.Getenv("SIGNET_PORT"); p != "" {
		return p
	}
	return "7766"
}

// verified reports whether the server at base is this user's Signet: it must
// prove it knows the secret in ~/.signet/secret before we send it anything.
func verified(base string, secret []byte) bool {
	nonce := newID()
	c := http.Client{Timeout: 2 * time.Second}
	r, err := c.Get(base + "/healthz?nonce=" + nonce)
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var h struct{ App, Proof string }
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&h) != nil || h.App != "signet" {
		return false
	}
	return hmac.Equal([]byte(h.Proof), []byte(proof(secret, nonce)))
}

// findEditor returns the address of this user's running editor, if there is one.
func findEditor(dir string, secret []byte) (string, bool) {
	var ports []string
	if b, err := os.ReadFile(editorPath(dir)); err == nil {
		var e editorInfo
		if json.Unmarshal(b, &e) == nil && e.Port > 0 {
			ports = append(ports, strconv.Itoa(e.Port))
		}
	}
	ports = append(ports, preferredPort())
	for _, p := range ports {
		base := "http://127.0.0.1:" + p
		if verified(base, secret) {
			return base, true
		}
	}
	return "", false
}

func call(method, url string, secret []byte, body []byte, ctype string, out any) error {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(secret))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	c := http.Client{Timeout: 60 * time.Second}
	r, err := c.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var res map[string]any
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if r.StatusCode != 200 {
		dec.Decode(&res)
		return fmt.Errorf("the editor said %d: %v", r.StatusCode, res["error"])
	}
	return dec.Decode(out)
}

func openEditor(files []string, dir string) error {
	var paths []string
	for _, f := range files {
		p, err := filepath.Abs(f)
		if err != nil {
			return err
		}
		if _, err := readPDF(p); err != nil {
			return err
		}
		paths = append(paths, p)
	}
	secret, err := loadSecret(dir)
	if err != nil {
		return err
	}
	base, ok := findEditor(dir, secret)
	if !ok {
		if base, err = startInBackground(dir, secret); err != nil {
			return err
		}
	}
	var ids []string
	for _, p := range paths {
		b, _ := readPDF(p)
		q := url.Values{"name": {filepath.Base(p)}, "path": {p}}
		var res struct{ ID string }
		if err := call("POST", base+"/api/docs?"+q.Encode(), secret, b, "application/pdf", &res); err != nil {
			return fmt.Errorf("could not open %s: %w", p, err)
		}
		ids = append(ids, res.ID)
	}
	var launch struct{ Code string }
	if err := call("POST", base+"/api/launch", secret, nil, "", &launch); err != nil {
		return err
	}
	q := url.Values{"k": {launch.Code}}
	if len(ids) > 0 {
		q.Set("doc", strings.Join(ids, ","))
	}
	u := base + "/?" + q.Encode()
	if os.Getenv("SIGNET_NO_WINDOW") == "" {
		if err := openWindow(u); err != nil {
			fmt.Fprintln(os.Stderr, "signet: could not open a window:", err)
		}
	}
	fmt.Println(u)
	return nil
}

// startInBackground runs `signet serve` detached, so double-clicking a PDF
// leaves no terminal behind, and waits until it answers with the right proof.
func startInBackground(dir string, secret []byte) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(dir, "signet.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 1<<20 { // keep the log small
		os.Rename(logPath, logPath+".old")
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	os.Remove(editorPath(dir))
	c := exec.Command(exe, "serve")
	c.Stdout, c.Stderr = logf, logf
	c.Env = append(os.Environ(), "SIGNET_DATA="+dir)
	detach(c)
	if err := c.Start(); err != nil {
		return "", err
	}
	c.Process.Release()
	for i := 0; i < 200; i++ {
		if base, ok := findEditor(dir, secret); ok {
			return base, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "", fmt.Errorf("the editor did not start; see %s", logPath)
}

// serve runs the editor until nothing has talked to it for SIGNET_IDLE minutes
// (default 10). The editor page pings it every minute while it is open.
func serve(dir string, logw *os.File) error {
	log.SetOutput(logw)
	ln, err := net.Listen("tcp", "127.0.0.1:"+preferredPort())
	if err != nil { // taken (another program, or another account's Signet): any free port will do
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	idle := 10 * time.Minute
	if m, err := strconv.Atoi(os.Getenv("SIGNET_IDLE")); err == nil && m > 0 {
		idle = time.Duration(m) * time.Minute
	}
	h, err := newServer(dir)
	if err != nil {
		return err
	}
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			last.Store(time.Now().UnixNano())
			h.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.Default(),
	}
	info, _ := json.Marshal(editorInfo{Port: port, PID: os.Getpid()})
	if err := writeAtomic(editorPath(dir), info, 0o600); err != nil {
		return err
	}
	defer func() { // forget our address, unless a newer editor has taken over
		if b, err := os.ReadFile(editorPath(dir)); err == nil && bytes.Equal(b, info) {
			os.Remove(editorPath(dir))
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Printf("signet %s editor on http://127.0.0.1:%d (data in %s)", version, port, dir)
	tick := time.NewTicker(time.Minute / 4)
	defer tick.Stop()
	for {
		select {
		case err := <-errc:
			return err
		case <-tick.C:
			if time.Since(time.Unix(0, last.Load())) > idle {
				log.Printf("idle for %s, stopping", idle)
				return srv.Close()
			}
		}
	}
}

// openWindow shows the editor in its own window when Chrome, Edge, Brave or
// Chromium is installed (app mode: no tabs, no address bar), else in the default
// browser. SIGNET_BROWSER=default always uses the default browser.
func openWindow(u string) error {
	if os.Getenv("SIGNET_BROWSER") != "default" {
		for _, b := range appBrowsers() {
			if runtime.GOOS == "darwin" {
				if _, err := os.Stat("/Applications/" + b + ".app"); err == nil {
					return exec.Command("open", "-na", b, "--args", "--app="+u).Start()
				}
				continue
			}
			if p := findExe(b); p != "" {
				return exec.Command(p, "--app="+u).Start()
			}
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

func appBrowsers() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"}
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`))
		}
		return out
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"}
	}
}

func findExe(name string) string {
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err == nil {
			return name
		}
		return ""
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}
