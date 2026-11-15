# Security

Signet holds people's signatures and personal details, so security bugs matter.

Please report them privately: use **Report a vulnerability** on the repository's Security tab (GitHub private advisory), not a public issue. You'll get an answer within a week.

What Signet promises:

- Your profile and signatures stay in `~/.signet` on your own computer (folder and files readable only by you). Signet makes no network connections of its own.
- The editor listens on `127.0.0.1` only. It refuses requests not addressed to localhost (DNS rebinding), cross-site requests and writes from other origins, and sends a strict Content-Security-Policy and no-framing headers.
- All data endpoints need either the secret in `~/.signet/secret` (the CLI) or a session cookie (HttpOnly, SameSite=Strict) that a window gets by redeeming a single-use launch code that expires after 2 minutes. Other accounts on a shared computer cannot read your data.
- The CLI checks the editor's identity (an HMAC challenge over that secret) before sending it anything, so a program squatting on the port can't receive your PDFs.
- Only the CLI can choose where a signed copy is written; the signed copy gets the same permissions as the original, and the original is never overwritten.

Out of scope: someone who can already run programs as you, or read your files, can read your signatures. That is true of any app that keeps them.

## Verifying a download

```sh
gh attestation verify signet-darwin-arm64 -R kahlkritzinger/signet
```

checks that the file was built by this repository's GitHub Actions from the tagged source. `checksums.txt` in each release lists every file's SHA-256; the install scripts check it.
