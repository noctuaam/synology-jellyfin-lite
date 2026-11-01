# Development and release

Edit Go and embedded HTML/JS under `server/`, not files installed on the NAS. Use any editor with Go support. The backend depends on pure-Go SQLite; ARM64 builds do not require a cross C compiler. Run format/vet/tests on the development computer, then cross-compile.

```powershell
python scripts/prepare_web.py /path/to/jellyfin-web.deb vendor-web
./server/check.ps1
./scripts/Build-Release.ps1 -Web ./vendor-web -Output ./dist
```

The pinned archive is Jellyfin Web 10.10.7+deb12 SHA256 `303bf4bf0892a422b75627c158f6177464ab216d0a431225277ce3fbffbbb08d`. The exact main bundle hash is validated too. Use the matching archive supplied with the release; do not silently substitute a newer build with the same version string. Upstream source is supplied separately in the release archive. The Go runtime applies explicit hash-checked client patches; upstream assets remain unchanged on disk.

For Linux, run `gofmt -l *.go`, `go vet ./...`, `go test ./...` inside server/, then `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-s -w' -o ../dist/nas-media-folders-arm64 .`. Run packaging/synology/build.py with explicit --binary, --web and --output arguments.

Use a separate JSON config and Data directory for local testing. Never point a development process at the live NAS database. The Web directory must contain the exact prepared client. New installs initialize with `-init -username admin`, printing a randomly generated password once; existing data must not be reinitialized. The server binds loopback unless configured otherwise.

For every release include the SPK, ARM64 binary, this project's complete source/scripts/docs, SHA256SUMS, the pinned upstream client archive and its matching preferred source archive. Preserve GPL and dependency notices. Run archive validation and review the source bundle for personal addresses, paths, usernames and credentials before publication. Publishing to GitHub is a separate user action.
