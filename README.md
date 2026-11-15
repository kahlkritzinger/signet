# Signet

**Fill, sign and initial PDFs. Nothing else.**

One small file for Mac, Windows and Linux. Double-click a PDF and it opens in Signet. Your signatures and details never leave your computer: no account, no cloud, no subscription. And everything it does, your AI can do from the terminal.

- **Fill** form fields, or type anywhere on the page
- **Sign** with your own signature: draw it, photograph it, or let Signet invent one; black, blue or red ink
- **Initial** every page in one go, each one slightly different, like by hand
- **Your details on a right-click:** name, ID number, address, anything you add (a car, a company)
- Dates, ticks and crosses, highlights; undo, copy, paste
- Saves `name-signed.pdf` next to the original. Nothing is flattened: form fields stay editable

## Install

**Mac or Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/kahlkritzinger/signet/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/kahlkritzinger/signet/main/install.ps1 | iex
```

**With Go:** `go install github.com/kahlkritzinger/signet@latest`. Or download the file for your computer from [Releases](https://github.com/kahlkritzinger/signet/releases).

Then make it your PDF app:

```sh
signet set-default
```

## Use it

Double-click a PDF, or run `signet lease.pdf`. Right-click the page for everything. The first time, open **Profile** (press P) to add your details, and make your signature (S) and initials (I).

Keys: **T** text · **S** signature · **I** initials · **D** date · **C** tick · **X** cross · **P** profile · **H** highlight · **⌘/Ctrl + Z, C, V, D** undo, copy, paste, duplicate

Signet opens in its own window when Chrome, Edge or Brave is installed, otherwise in your browser. It keeps running in the background and stops by itself 10 minutes after you close it.

## For your AI (and scripts)

Tell Claude Code, Codex, Cursor or any terminal AI: *"use `signet` to fill in and sign this PDF; show me before you send it anywhere"*. It has everything it needs:

```sh
signet inspect lease.pdf             # Markdown: pages, form fields (name, x, y), text
signet find lease.pdf "Signature:"   # where a phrase is: page, x, y
signet profile                       # what it can fill in: "Full name", "ID number", "Address", ...
signet bank                          # your signatures and initials
signet apply lease.pdf plan.json     # fill, write, sign, initial -> lease-signed.pdf
```

`plan.json` (coordinates are PDF points from the top-left of the page):

```json
{"fields": {"tenant.name": {"profile": "Full name"}, "agree": true, "term": "12 months"},
 "items": [
   {"type": "sign",    "page": 3, "x": 130, "y": 700},
   {"type": "date",    "page": 3, "x": 330, "y": 702},
   {"type": "text",    "page": 1, "x": 72,  "y": 300, "profile": "ID number"},
   {"type": "initial", "page": "every", "x": 500, "y": 780},
   {"type": "check",   "page": 2, "x": 60,  "y": 410}]}
```

Types: `text`, `date`, `sign`, `initial`, `check`, `cross`. Inks: `black`, `blue` (signatures default), `red`. `signet help` has the rest. Your AI can use your signature without ever seeing it: `apply` reads it from your computer.

## Private by design

Your profile and signatures are two files in `~/.signet` (`SIGNET_DATA` to move them). The editor runs on `127.0.0.1` and only answers requests addressed to your own machine, so websites you visit can't reach it. Signet never connects to the internet.

## How it works

One Go binary with the editor built in. The editor is plain JavaScript in `web/`: [pdf.js](https://mozilla.github.io/pdf.js/) shows the pages and reads forms, [pdf-lib](https://pdf-lib.js.org/) writes the result. The CLI uses [pdfcpu](https://github.com/pdfcpu/pdfcpu) to fill forms and add marks, and [ledongthuc/pdf](https://github.com/ledongthuc/pdf) to read text positions.

| File | What |
|---|---|
| `main.go` | commands, starting the editor, opening its window |
| `server.go` | the editor's local server (store, documents, save next to the original) |
| `pdfops.go` | `inspect`, `find`, `apply` |
| `store.go` | profile and signature bank |
| `setdefault.go` | `signet set-default` per platform |
| `web/` | the editor |

## Not yet

- Scanned PDFs: no text to search, but you can still place everything by hand
- Locked PDFs need their password; some encrypted PDFs can't be saved yet
- Dropdown fields filled by the CLI may look empty in some viewers until clicked (the value is saved)
- The Mac and Windows downloads aren't code-signed, so the first open may need *Open anyway* (Mac: System Settings > Privacy & Security). The install scripts avoid this.

## Contributing

Yes please. Small and simple wins: every new button needs a reason. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

[MIT](LICENSE). Third-party licences in [NOTICE](NOTICE).
