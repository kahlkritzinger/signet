# Contributing to Signet

Thanks for helping. Signet does three things (fill, sign, initial) and should stay light, fast and obvious. Before adding a feature, ask: would someone signing a lease on a Friday afternoon miss it?

## Build and test

You need Go 1.26 or newer.

```sh
go test ./...                 # tests
go run . testdata/sample.pdf  # the editor, with the sample lease
go run . help                 # the CLI
```

Use a throwaway data folder while developing, so your real signatures stay out of it: `SIGNET_DATA=/tmp/signet-dev go run . testdata/sample.pdf`.

The editor (`web/`) is plain JavaScript with no build step: edit, rebuild, reload.

## Pull requests

- One change per pull request, with a test where it can have one (`signet_test.go`).
- `gofmt`, `go vet` and `go test ./...` must pass (CI runs them on Mac, Windows and Linux).
- If it changes what gets drawn on a page, add a before/after screenshot.
- Keep the binary small: new dependencies need a good reason.
- Never commit real signatures, ID numbers or personal PDFs. Use `testdata/sample.pdf` or make a new test PDF.

## Good first issues

Look for the `good first issue` label, or pick something from **Not yet** in the README.

## Releases

Maintainers tag `vX.Y.Z` on `main`; GitHub Actions builds every platform and publishes the release.
