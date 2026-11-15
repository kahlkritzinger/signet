// Signet: fill, sign and initial PDFs. An editor on your own machine, and a
// CLI for you and your AI. Your signatures and profile never leave your computer.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
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
	"time"
)

var version = "dev" // set by the release build

const usage = `Signet: fill, sign and initial PDFs. Your signatures stay on your machine.

  signet [open] [FILE.pdf ...]       the editor, in its own window
  signet inspect FILE [--pages 5]    Markdown: pages, form fields (name, x, y), text
  signet find FILE "Signature:"      where a phrase is (page, x, y), to place things by it
  signet profile                     what can be filled in: "Full name", "ID number", ...
  signet bank                        your signatures and initials
  signet apply FILE PLAN.json [--out OUT]   fill, write, sign, initial -> FILE-signed.pdf
  signet version

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
editor; the CLI reads them. The editor listens on 127.0.0.1:7766 (SIGNET_PORT).
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
	switch cmd {
	case "open":
		return openEditor(args, dir)
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

func port() string {
	if p := os.Getenv("SIGNET_PORT"); p != "" {
		return p
	}
	return "7766"
}

func running(base string) bool {
	c := http.Client{Timeout: time.Second}
	r, err := c.Get(base + "/healthz")
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var h struct{ App string }
	return json.NewDecoder(r.Body).Decode(&h) == nil && h.App == "signet"
}

func send(base, path string) (string, error) {
	b, err := readPDF(path)
	if err != nil {
		return "", err
	}
	q := url.Values{"name": {filepath.Base(path)}, "path": {path}}
	r, err := http.Post(base+"/api/docs?"+q.Encode(), "application/pdf", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var res struct{ ID, Error string }
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil || res.ID == "" {
		return "", fmt.Errorf("the editor refused %s: %s", path, res.Error)
	}
	return res.ID, nil
}

func openEditor(files []string, dir string) error {
	base := "http://127.0.0.1:" + port()
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
	started := false
	if !running(base) {
		ln, err := net.Listen("tcp", "127.0.0.1:"+port())
		if err != nil {
			return fmt.Errorf("port %s is busy (set SIGNET_PORT): %w", port(), err)
		}
		go http.Serve(ln, newServer(dir))
		started = true
	}
	var ids []string
	for _, p := range paths {
		id, err := send(base, p)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	u := base + "/"
	if len(ids) > 0 {
		u += "?doc=" + strings.Join(ids, ",")
	}
	if os.Getenv("SIGNET_NO_WINDOW") == "" {
		if err := openWindow(u); err != nil {
			fmt.Fprintln(os.Stderr, "signet: could not open a window:", err)
		}
	}
	fmt.Println(u)
	if !started {
		return nil // the running editor has the files now
	}
	fmt.Fprintf(os.Stderr, "Signet is running (data in %s). Save writes NAME-signed.pdf next to the original. Ctrl+C to stop.\n", dir)
	select {}
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
