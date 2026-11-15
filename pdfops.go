package main

// Reading and changing PDFs for the CLI. Coordinates are PDF points with the
// origin at the TOP-LEFT of the page, like the editor. Nothing is flattened:
// form fields stay fields, marks are added on top.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	lpdf "github.com/ledongthuc/pdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/font"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var bg = context.Background()

// pdfcpu would otherwise create a settings folder in your home folder, which
// fails where there is none (in a container, under a read-only home).
func init() { api.DisableConfigDir() }

var inks = map[string]color.NRGBA{
	"black": {0x16, 0x16, 0x1a, 255},
	"blue":  {0x1f, 0x3f, 0xae, 255},
	"red":   {0xc9, 0x21, 0x1e, 255},
}

func inkHex(ink string) string {
	c := inks[ink]
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

var (
	defaultWidth = map[string]float64{"sig": 150, "ini": 46} // points
	maxHeight    = map[string]float64{"sig": 50, "ini": 24}  // ...but by default no taller than a line
	dateFormats  = map[string]string{"long": "2 January 2006", "iso": "2006-01-02", "dmy": "02/01/2006", "short": "2 Jan 2006"}
)

// pdfPassword opens locked PDFs (--password, or SIGNET_PDF_PASSWORD).
var pdfPassword = os.Getenv("SIGNET_PDF_PASSWORD")

func conf() *model.Configuration {
	c := model.NewDefaultConfiguration()
	c.ValidationMode = model.ValidationRelaxed
	c.UserPW, c.OwnerPW = pdfPassword, pdfPassword
	return c
}

const maxInput = 200 << 20

func readPDF(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() || info.Size() > maxInput {
		return nil, fmt.Errorf("%s is not a PDF Signet can open (folder, or over 200 MB)", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(bytes.TrimLeft(b[:min(len(b), 1024)], "\x00\t\r\n "), []byte("%PDF")) {
		return nil, fmt.Errorf("%s is not a PDF", path)
	}
	return b, nil
}

// friendly turns library errors into something a person (or an AI) can act on.
func friendly(err error) error {
	if err == nil {
		return nil
	}
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "password") || strings.Contains(m, "encrypt"):
		if pdfPassword == "" {
			return fmt.Errorf("this PDF is locked: add --password (or set SIGNET_PDF_PASSWORD)")
		}
		return fmt.Errorf("that password does not open this PDF")
	}
	return err
}

// safely runs f and turns a crash inside a PDF library into an error, so one
// damaged or unusual file can never take Signet down.
func safely[T any](what string, f func() (T, error)) (out T, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("could not %s: the PDF looks damaged or unusual, or this is a bug in Signet (%v)", what, v)
		}
	}()
	out, err = f()
	return out, friendly(err)
}

// ---------- reading ----------

type FieldInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Page    int      `json:"page"`
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	W       float64  `json:"w"`
	H       float64  `json:"h"`
	Value   string   `json:"value"`
	Options []string `json:"options,omitempty"`
}

var fieldTypes = map[form.FieldType]string{
	form.FTText: "text", form.FTDate: "date", form.FTCheckBox: "checkbox",
	form.FTComboBox: "dropdown", form.FTListBox: "list", form.FTRadioButtonGroup: "radio",
}

func fields(b []byte) ([]FieldInfo, error) {
	ff, err := api.FormFields(bg, bytes.NewReader(b), conf())
	if err != nil {
		if strings.Contains(err.Error(), "no form") || strings.Contains(err.Error(), "form") {
			return nil, nil
		}
		return nil, err
	}
	rects := widgetRects(b)
	var out []FieldInfo
	for _, f := range ff {
		fi := FieldInfo{Name: f.Name, Type: fieldTypes[f.Typ], Value: f.V}
		if fi.Type == "" {
			fi.Type = "other"
		}
		if len(f.Pages) > 0 {
			fi.Page = f.Pages[0]
		}
		if f.Opts != "" {
			fi.Options = strings.Split(f.Opts, ",")
		}
		if r, ok := rects[f.Name]; ok {
			fi.Page, fi.X, fi.Y, fi.W, fi.H = r.page, r.x, r.y, r.w, r.h
		}
		out = append(out, fi)
	}
	return out, nil
}

type rect struct {
	page       int
	x, y, w, h float64
}

// widgetRects finds where each form field sits: full field name -> first widget's rectangle.
func widgetRects(b []byte) map[string]rect {
	out := map[string]rect{}
	ctx, err := api.ReadAndValidate(bg, bytes.NewReader(b), conf())
	if err != nil {
		return out
	}
	x := ctx.XRefTable
	dims, err := api.PageDims(bg, bytes.NewReader(b), conf())
	if err != nil {
		return out
	}
	for p := 1; p <= len(dims); p++ {
		pd, _, _, err := x.PageDict(bg, p, false)
		if err != nil || pd == nil || p > len(dims) {
			continue
		}
		annots, _ := x.DereferenceArray(pd["Annots"])
		for _, a := range annots {
			d, err := x.DereferenceDict(a)
			if err != nil || d == nil {
				continue
			}
			if st := d.NameEntry("Subtype"); st == nil || *st != "Widget" {
				continue
			}
			name := fullName(x, d)
			r, _ := x.DereferenceArray(d["Rect"])
			if name == "" || len(r) != 4 {
				continue
			}
			x1, y1, x2, y2 := num(x, r[0]), num(x, r[1]), num(x, r[2]), num(x, r[3])
			if _, seen := out[name]; !seen {
				out[name] = rect{p, round1(math.Min(x1, x2)), round1(dims[p-1].Height - math.Max(y1, y2)), round1(math.Abs(x2 - x1)), round1(math.Abs(y2 - y1))}
			}
		}
	}
	return out
}

func fullName(x *model.XRefTable, d types.Dict) string {
	var parts []string
	for i := 0; d != nil && i < 20; i++ {
		if t, err := x.DereferenceStringOrHexLiteral(d["T"], model.V10, nil); err == nil && t != "" {
			parts = append([]string{t}, parts...)
		}
		pd, err := x.DereferenceDict(d["Parent"])
		if err != nil {
			break
		}
		d = pd
	}
	return strings.Join(parts, ".")
}

func num(x *model.XRefTable, o types.Object) float64 {
	o, _ = x.Dereference(o)
	switch v := o.(type) {
	case types.Float:
		return float64(v)
	case types.Integer:
		return float64(v)
	}
	return 0
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// Line is one line of text on a page, with where each character starts.
type Line struct {
	Text string
	Xs   []float64 // per rune
	Ends []float64 // per rune
	Top  float64   // from the top of the page
	Size float64
}

func pageLines(path string) ([][]Line, []types.Dim, error) {
	b, err := readPDF(path)
	if err != nil {
		return nil, nil, err
	}
	dims, err := api.PageDims(bg, bytes.NewReader(b), conf())
	if err != nil {
		return nil, nil, err
	}
	var r *lpdf.Reader
	if pdfPassword != "" {
		r, err = lpdf.NewReaderEncrypted(bytes.NewReader(b), int64(len(b)), func() string { return pdfPassword })
	} else {
		r, err = lpdf.NewReader(bytes.NewReader(b), int64(len(b)))
	}
	if err != nil {
		return nil, dims, nil // unreadable text layer: fields and placing still work
	}
	pages := make([][]Line, len(dims))
	for p := 1; p <= r.NumPage() && p <= len(dims); p++ {
		pages[p-1] = linesOf(r.Page(p), dims[p-1].Height)
	}
	return pages, dims, nil
}

type glyph struct {
	s             string
	x, w, y, size float64
}

func linesOf(pg lpdf.Page, height float64) (lines []Line) {
	defer func() { recover() }() // a broken content stream must not stop the rest
	var gs []glyph
	runX, runY, acc := math.NaN(), math.NaN(), 0.0
	for _, t := range pg.Content().Text {
		w := t.W
		if w == 0 { // standard fonts carry no widths: measure them ourselves
			w = width(t.S, t.Font, t.FontSize)
		}
		x := t.X
		if t.X == runX && t.Y == runY {
			x = runX + acc
		} else {
			runX, runY, acc = t.X, t.Y, 0
		}
		acc += w
		gs = append(gs, glyph{t.S, x, w, t.Y, t.FontSize})
	}
	sort.SliceStable(gs, func(i, j int) bool {
		if math.Abs(gs[i].y-gs[j].y) > 2 {
			return gs[i].y > gs[j].y
		}
		return gs[i].x < gs[j].x
	})
	var cur *Line
	lastY, lastEnd := math.NaN(), 0.0
	for _, g := range gs {
		if cur == nil || math.Abs(g.y-lastY) > 2 {
			lines = append(lines, Line{Top: round1(height - g.y - g.size*0.8), Size: g.size})
			cur = &lines[len(lines)-1]
			lastY = g.y
		} else if g.x-lastEnd > g.size*0.25 && !strings.HasSuffix(cur.Text, " ") && g.s != " " {
			cur.Text += " "
			cur.Xs = append(cur.Xs, lastEnd)
			cur.Ends = append(cur.Ends, g.x)
		}
		for _, r := range g.s {
			cur.Text += string(r)
			cur.Xs = append(cur.Xs, g.x)
			cur.Ends = append(cur.Ends, g.x+g.w)
		}
		lastEnd = g.x + g.w
	}
	return lines
}

func width(s, fontName string, size float64) float64 {
	name := fontName
	if i := strings.Index(name, "+"); i >= 0 {
		name = name[i+1:]
	}
	total := 0.0
	for _, r := range s {
		if font.IsCoreFont(name) {
			if u, err := font.CharWidth(bg, name, r); err == nil {
				total += float64(u) / 1000 * size
				continue
			}
		}
		total += 0.5 * size
	}
	return total
}

type Hit struct {
	Page int     `json:"page"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	Line string  `json:"line"`
}

func find(path, needle string) ([]Hit, error) {
	return safely("search the PDF", func() ([]Hit, error) { return findUnsafe(path, needle) })
}

func findUnsafe(path, needle string) ([]Hit, error) {
	pages, _, err := pageLines(path)
	if err != nil {
		return nil, err
	}
	n := []rune(strings.ToLower(needle))
	var hits []Hit
	for p, lines := range pages {
		for _, l := range lines {
			rs := []rune(strings.ToLower(l.Text))
			for i := 0; i+len(n) <= len(rs) && len(n) > 0; i++ {
				if string(rs[i:i+len(n)]) == string(n) {
					hits = append(hits, Hit{p + 1, round1(l.Xs[i]), l.Top, round1(l.Ends[i+len(n)-1] - l.Xs[i]), round1(l.Size), strings.TrimSpace(l.Text)})
				}
			}
		}
	}
	return hits, nil
}

func inspect(path string, maxPages int) (string, error) {
	return safely("read the PDF", func() (string, error) { return inspectUnsafe(path, maxPages) })
}

func inspectUnsafe(path string, maxPages int) (string, error) {
	b, err := readPDF(path)
	if err != nil {
		return "", err
	}
	pages, dims, err := pageLines(path)
	if err != nil {
		return "", err
	}
	ff, err := fields(b)
	if err != nil {
		return "", err
	}
	var o strings.Builder
	sizes := map[string]bool{}
	for _, d := range dims {
		sizes[fmt.Sprintf("%.0fx%.0f pt", d.Width, d.Height)] = true
	}
	var sz []string
	for s := range sizes {
		sz = append(sz, s)
	}
	sort.Strings(sz)
	fmt.Fprintf(&o, "# %s\n\n%d pages, %s.\n\n## Form fields\n\n", filepath.Base(path), len(dims), strings.Join(sz, ", "))
	if len(ff) == 0 {
		o.WriteString("None. Place text, signatures and initials at x, y instead (`signet find` gives positions).\n")
	} else {
		o.WriteString("| name | type | page | x | y | w | h | value | options |\n|---|---|---|---|---|---|---|---|---|\n")
		for _, f := range ff {
			fmt.Fprintf(&o, "| %s | %s | %d | %g | %g | %g | %g | %s | %s |\n", f.Name, f.Type, f.Page, f.X, f.Y, f.W, f.H, f.Value, strings.Join(f.Options, ", "))
		}
	}
	for i := 0; i < len(pages) && i < maxPages; i++ {
		fmt.Fprintf(&o, "\n## Page %d\n\n", i+1)
		if len(pages[i]) == 0 {
			o.WriteString("(no text layer: probably a scan)\n")
		}
		for _, l := range pages[i] {
			if t := strings.TrimSpace(l.Text); t != "" {
				o.WriteString(t + "\n")
			}
		}
	}
	if len(pages) > maxPages {
		fmt.Fprintf(&o, "\n_%d more pages not shown (--pages %d)._\n", len(pages)-maxPages, len(pages))
	}
	return o.String(), nil
}

// ---------- writing ----------

// Plan is what `signet apply` does to a PDF. See the README for the format.
type Plan struct {
	Fields map[string]any `json:"fields"`
	Items  []Item         `json:"items"`
}

type Item struct {
	Type    string  `json:"type"` // text, date, sign, initial, check, cross
	Page    any     `json:"page"` // 1-based number, or "every"
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Text    string  `json:"text,omitempty"`
	Profile string  `json:"profile,omitempty"` // fill text from this profile label
	Size    float64 `json:"size,omitempty"`    // text size, or tick size, in points
	Width   float64 `json:"width,omitempty"`   // signature / initials width in points
	Ink     string  `json:"ink,omitempty"`     // black, blue, red
	Date    string  `json:"date,omitempty"`    // YYYY-MM-DD, default today
	Format  string  `json:"format,omitempty"`  // long, iso, dmy, short
	ID      string  `json:"id,omitempty"`      // which bank mark (default: the newest)
}

type Result struct {
	Saved string   `json:"saved"`
	Done  []string `json:"done"`
}

func apply(path string, plan Plan, out, dir string, rnd *rand.Rand) (Result, error) {
	return safely("change the PDF", func() (Result, error) { return applyUnsafe(path, plan, out, dir, rnd) })
}

func applyUnsafe(path string, plan Plan, out, dir string, rnd *rand.Rand) (Result, error) {
	var res Result
	b, err := readPDF(path)
	if err != nil {
		return res, err
	}
	if out == "" {
		out = strings.TrimSuffix(path, filepath.Ext(path)) + "-signed.pdf"
	}
	if same(path, out) {
		return res, fmt.Errorf("refusing to overwrite the original: choose another --out")
	}
	dims, err := api.PageDims(bg, bytes.NewReader(b), conf())
	if err != nil {
		return res, err
	}
	n := len(dims)
	if err := checkItems(plan.Items, dims); err != nil {
		return res, err
	}
	jit := func(a float64) float64 { return (rnd.Float64()*2 - 1) * a }

	var entries []Entry
	profile := func(label string) (string, error) {
		if entries == nil {
			p, err := loadProfile(dir)
			if err != nil {
				return "", err
			}
			entries = profileEntries(p)
		}
		return lookup(entries, label)
	}

	// 1. form fields
	if len(plan.Fields) > 0 {
		ff, err := fields(b)
		if err != nil {
			return res, err
		}
		types := map[string]string{}
		for _, f := range ff {
			types[f.Name] = f.Type
		}
		groups := map[string][]map[string]any{}
		names := make([]string, 0, len(plan.Fields))
		for k := range plan.Fields {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			v := plan.Fields[name]
			if m, ok := v.(map[string]any); ok {
				label, _ := m["profile"].(string)
				s, err := profile(label)
				if err != nil {
					return res, err
				}
				v = s
			}
			switch types[name] {
			case "":
				return res, fmt.Errorf("no form field named %q (see `signet inspect`)", name)
			case "checkbox":
				groups["checkbox"] = append(groups["checkbox"], map[string]any{"name": name, "value": truthy(v)})
			case "dropdown":
				groups["combobox"] = append(groups["combobox"], map[string]any{"name": name, "value": fmt.Sprint(v)})
			case "radio":
				groups["radiobuttongroup"] = append(groups["radiobuttongroup"], map[string]any{"name": name, "value": fmt.Sprint(v)})
			case "date":
				groups["datefield"] = append(groups["datefield"], map[string]any{"name": name, "value": fmt.Sprint(v)})
			case "text":
				groups["textfield"] = append(groups["textfield"], map[string]any{"name": name, "value": fmt.Sprint(v)})
			default:
				return res, fmt.Errorf("field %q is a %s field, which can't be filled yet", name, types[name])
			}
			res.Done = append(res.Done, "field "+name)
		}
		js, _ := json.Marshal(map[string]any{"forms": []any{groups}})
		var filled bytes.Buffer
		if err := api.FillForm(bg, bytes.NewReader(b), bytes.NewReader(js), &filled, conf()); err != nil {
			return res, fmt.Errorf("filling the form: %w", err)
		}
		b = filled.Bytes()
	}

	// 2. marks on top
	var bank []Mark
	marks := map[int][]*model.Watermark{}
	add := func(p int, wm *model.Watermark) { marks[p] = append(marks[p], wm) }
	for _, it := range plan.Items {
		pages, err := targetPages(it.Page, n)
		if err != nil {
			return res, err
		}
		ink := it.Ink
		switch {
		case ink == "" && (it.Type == "sign" || it.Type == "initial"):
			ink = "blue"
		case ink == "":
			ink = "black"
		case inks[ink].A == 0:
			return res, fmt.Errorf("ink must be black, blue or red, not %q", ink)
		}
		label := fmt.Sprintf("%s page %v", it.Type, pageLabel(it.Page))
		switch it.Type {
		case "text", "date":
			text := it.Text
			if it.Type == "date" {
				d := time.Now()
				if it.Date != "" {
					if d, err = time.Parse("2006-01-02", it.Date); err != nil {
						return res, fmt.Errorf("date must be YYYY-MM-DD, not %q", it.Date)
					}
				}
				f := dateFormats[it.Format]
				if f == "" {
					f = dateFormats["long"]
				}
				text = d.Format(f)
			} else if it.Profile != "" {
				if text, err = profile(it.Profile); err != nil {
					return res, err
				}
			}
			text = strings.ReplaceAll(text, "\r", "")
			if bad := undrawable(text); bad != "" {
				return res, fmt.Errorf("the built-in font can't draw %q in %q; use plain Latin letters there", bad, text)
			}
			size := it.Size
			if size == 0 {
				size = 11
			}
			for i, line := range strings.Split(text, "\n") {
				if line == "" {
					continue
				}
				desc := fmt.Sprintf("font:Helvetica, points:%d, pos:tl, off:%.2f %.2f, scale:1 abs, rot:0, fillc:%s, op:1",
					int(math.Round(size)), it.X, -(it.Y + float64(i)*size*1.2), inkHex(ink))
				wm, err := api.TextWatermark(bg, line, desc, true, false, types.POINTS, conf())
				if err != nil {
					return res, err
				}
				for _, p := range pages {
					add(p, wm)
				}
			}
		case "check", "cross":
			size := it.Size
			if size == 0 {
				size = 12
			}
			glyph := "4" // ✔ in ZapfDingbats
			if it.Type == "cross" {
				glyph = "8" // ✘
			}
			desc := fmt.Sprintf("font:ZapfDingbats, points:%d, pos:tl, off:%.2f %.2f, scale:1 abs, rot:0, fillc:%s, op:1", int(math.Round(size)), it.X, -it.Y, inkHex(ink))
			wm, err := api.TextWatermark(bg, glyph, desc, true, false, types.POINTS, conf())
			if err != nil {
				return res, err
			}
			for _, p := range pages {
				add(p, wm)
			}
		case "sign", "initial":
			kind := map[string]string{"sign": "sig", "initial": "ini"}[it.Type]
			if bank == nil {
				if bank, err = loadBank(dir); err != nil {
					return res, err
				}
			}
			m, err := pickMark(bank, kind, it.ID)
			if err != nil {
				return res, err
			}
			pngBytes, iw, _, err := tint(m.Mask, ink)
			if err != nil {
				return res, err
			}
			ar := m.Ar
			if ar <= 0 {
				ar = 3
			}
			w := it.Width
			if w == 0 {
				w = math.Min(defaultWidth[kind], maxHeight[kind]*ar)
			}
			many := len(pages) > 1 // initials on every page: each one a little different, like by hand
			for _, p := range pages {
				ww, dx, dy := w, 0.0, 0.0
				if many {
					ww, dx, dy = w*(1+jit(0.04)), jit(1.5), jit(1.5)
				}
				rot := jit(map[string]float64{"sig": 1.5, "ini": 3}[kind])
				desc := fmt.Sprintf("pos:tl, off:%.2f %.2f, scale:%.4f abs, rot:%.2f, op:1", it.X+dx, -(it.Y + dy), ww/float64(iw), rot)
				wm, err := api.ImageWatermarkForReader(bg, bytes.NewReader(pngBytes), desc, true, false, types.POINTS)
				if err != nil {
					return res, err
				}
				add(p, wm)
			}
		default:
			return res, fmt.Errorf("unknown item type %q (text, date, sign, initial, check, cross)", it.Type)
		}
		res.Done = append(res.Done, label)
	}
	if len(marks) > 0 {
		var outBuf bytes.Buffer
		if err := api.AddWatermarksSliceMap(bg, bytes.NewReader(b), &outBuf, marks, conf()); err != nil {
			return res, err
		}
		b = outBuf.Bytes()
	}
	if err := api.Validate(bg, bytes.NewReader(b), conf(), nil); err != nil { // never write a broken PDF
		return res, fmt.Errorf("the result did not check out as a valid PDF, nothing was written: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(out, b, mode); err != nil {
		return res, err
	}
	res.Saved = out
	if res.Done == nil {
		res.Done = []string{}
	}
	return res, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		s := strings.ToLower(x)
		return s == "true" || s == "yes" || s == "on" || s == "1" || s == "x"
	}
	return false
}

func targetPages(p any, n int) ([]int, error) {
	switch v := p.(type) {
	case nil:
		return []int{1}, nil
	case string:
		if v == "every" {
			all := make([]int, n)
			for i := range all {
				all[i] = i + 1
			}
			return all, nil
		}
	case float64:
		if v == math.Trunc(v) && v >= 1 && int(v) <= n {
			return []int{int(v)}, nil
		}
	}
	return nil, fmt.Errorf("page %v is not in 1..%d (or \"every\")", p, n)
}

func pageLabel(p any) string {
	if p == nil {
		return "1"
	}
	return fmt.Sprint(p)
}

func pickMark(bank []Mark, kind, id string) (Mark, error) {
	var found []Mark
	for _, m := range bank {
		if m.Kind == kind && (id == "" || m.ID == id) {
			found = append(found, m)
		}
	}
	if len(found) == 0 {
		what := map[string]string{"sig": "signature", "ini": "initials"}[kind]
		return Mark{}, fmt.Errorf("no %s in your bank yet: run `signet` and make one (press %s)", what, map[string]string{"sig": "S", "ini": "I"}[kind])
	}
	return found[len(found)-1], nil // the newest, like the editor
}

// tint turns a bank mark (alpha = ink) into a PNG in the given ink.
func tint(dataURL, ink string) ([]byte, int, int, error) {
	_, b64, ok := strings.Cut(dataURL, ",")
	if !ok {
		return nil, 0, 0, fmt.Errorf("bad mark in bank")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("bad mark in bank: %w", err)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("bad mark in bank: %w", err)
	}
	c := inks[ink]
	r := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			_, _, _, a := src.At(x, y).RGBA()
			dst.SetNRGBA(x-r.Min.X, y-r.Min.Y, color.NRGBA{c.R, c.G, c.B, uint8(a >> 8)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), r.Dx(), r.Dy(), nil
}

// same reports whether a and b are the same file (or would be).
func same(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 == nil && err2 == nil && aa == bb {
		return true
	}
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}

// checkItems catches the usual mistakes before anything is drawn: a position
// off the page, a silly size.
func checkItems(items []Item, dims []types.Dim) error {
	for i, it := range items {
		pages, err := targetPages(it.Page, len(dims))
		if err != nil {
			return fmt.Errorf("item %d: %w", i+1, err)
		}
		for _, p := range pages {
			d := dims[p-1]
			if it.X < 0 || it.Y < 0 || it.X >= d.Width || it.Y >= d.Height {
				return fmt.Errorf("item %d (%s): x %g, y %g is off page %d, which is %.0f x %.0f points", i+1, it.Type, it.X, it.Y, p, d.Width, d.Height)
			}
		}
		if it.Size < 0 || it.Size > 200 {
			return fmt.Errorf("item %d: size %g should be between 1 and 200 points", i+1, it.Size)
		}
		if it.Width < 0 || it.Width > 2000 {
			return fmt.Errorf("item %d: width %g should be between 1 and 2000 points", i+1, it.Width)
		}
	}
	return nil
}

// undrawable returns the characters the built-in Helvetica (WinAnsi) can't draw.
func undrawable(s string) string {
	const extra = "€‚ƒ„…†‡ˆ‰Š‹ŒŽ‘’“”•–—˜™š›œžŸ"
	var bad []rune
	for _, r := range s {
		ok := r == '\n' || (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) || strings.ContainsRune(extra, r)
		if !ok && !strings.ContainsRune(string(bad), r) {
			bad = append(bad, r)
		}
	}
	return string(bad)
}
