# Go code conventions

Follow [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) and
[Effective Go](https://go.dev/doc/effective_go). These apply to the maintained
server in this directory; do not reformat vendored dependencies or upstream
Jellyfin Web assets.

- Run `gofmt` on every changed Go file. Use standard Go tabs and import grouping.
- Keep structs and large JSON response literals readable: one field or keyed
  entry per line. Do not compress whole responses, handlers or declarations into
  single lines. There is no arbitrary maximum line length; protocol strings and
  version-checked JavaScript patch strings can stay intact.
- Prefer descriptive names in handlers and persistence code; use `err` for
  errors. Short names are fine in small loops and conventional receivers.
- Check errors from I/O, database queries and decoding. Explain any intentional
  ignored error locally. Keep error handling early and the successful path flat.
- Add doc comments to exported declarations and explain non-obvious compatibility
  behavior. Comments should describe why, rather than restate the statement.
- Preserve JSON field names, stable identifiers, protocol behavior and existing
  data during readability changes. Do not rename serialized struct fields or
  replace JSON empty arrays with null as a style cleanup.
- Prefer concrete types for internal data. Keep dynamic maps at the Jellyfin
  protocol boundary where compatibility requires them.
- Before handing off Go changes, run `./check.ps1` (or the equivalent `gofmt -l`,
  `go vet ./...`, and `go test ./...` on another platform). Fix failures before
  packaging. Do not add lint dependencies to the NAS runtime.

Existing code is being brought into these conventions incrementally. Passing
gofmt/vet/tests does not imply that every naming or readability issue is fixed.
