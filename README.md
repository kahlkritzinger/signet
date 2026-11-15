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

Every release file is checksummed and has a signed build attestation: `gh attestation verify signet-darwin-arm64 -R kahlkritzinger/signet` proves it was built by this repository's GitHub Actions from this source.

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

Types: `text`, `date`, `sign`, `initial`, `check`, `cross`. Inks: `black`, `blue` (signatures default), `red`. Locked PDF: add `--password X`. Mistakes (a position off the page, characters the built-in font can't draw) are caught before anything is written. `signet help` has the rest. Your AI can use your signature without ever seeing it: `apply` reads it from your computer.

## Private by design

- Your profile and signatures are two files in `~/.signet` (`SIGNET_DATA` to move them), readable only by you.
- The editor listens on `127.0.0.1` only. Every request for your data must prove it's you: the CLI with a secret only your account can read (`~/.signet/secret`), the editor window with a one-time launch code. Other accounts on the same computer and websites you visit get nothing.
- Before the CLI sends a PDF to the editor, the editor must prove it's really your Signet, so another program on the port can't intercept it.
- Signet never connects to the internet. (On a server behind Cloudflare Access it fetches Access's public signing keys, nothing else.)

## Your data is safe

- Saves are crash-safe: a power cut leaves the old file or the new one, never half of each.
- Every change to your profile or signatures keeps the previous version in `~/.signet/backups` (the last 10). A damaged file is restored from the newest good backup automatically.
- `signet apply` never touches the original, checks the result is a valid PDF before writing it, and stops with a clear message on a damaged, locked or unusual PDF instead of crashing or hanging.

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

## Run it on a server

To reach Signet from your phone, run it on a server behind [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/policies/access/) (Zero Trust), which signs you in. Signet checks Access's signed token on every request and lets in only the emails you name. Without that it refuses to listen anywhere but `127.0.0.1`.

```yaml
# compose.yaml
services:
  signet:
    image: ghcr.io/kahlkritzinger/signet:latest
    restart: unless-stopped
    user: "1000:1000"            # who owns ./data on the host
    environment:
      SIGNET_ACCESS_TEAM: myteam.cloudflareaccess.com
      SIGNET_ACCESS_AUD: <your Access application's AUD tag>
      SIGNET_ACCESS_EMAIL: me@example.com
      SIGNET_HOSTS: signet.example.com
    volumes:
      - ./data:/data
    ports:
      - "127.0.0.1:8080:8080"    # reach it through a Cloudflare Tunnel, not the open internet
```

The image is tiny (just the binary, not root), logs one JSON object per line, and has a health check. On the server, the CLI uses the same data: `docker compose exec signet /signet profile`, or run the binary with `SIGNET_DATA` pointing at `./data`. In this mode Signet's only outgoing connection is fetching Access's public signing keys.

## Not yet

- Scanned PDFs: no text to search, but you can still place everything by hand
- Locked PDFs: the CLI takes `--password`; the editor asks for it, but some encrypted PDFs can't be saved yet
- Text placed by the CLI uses Helvetica, so it's limited to Latin letters (accents are fine)
- Dropdown fields filled by the CLI may look empty in some viewers until clicked (the value is saved)
- The Mac and Windows downloads aren't code-signed, so the first open may need *Open anyway* (Mac: System Settings > Privacy & Security). The install scripts avoid this.

## Contributing

Yes please. Small and simple wins: every new button needs a reason. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

[MIT](LICENSE). Third-party licences in [NOTICE](NOTICE).
