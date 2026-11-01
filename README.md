# Synology Jellyfin Lite

An independent lightweight media server for Jellyfin clients on Synology NAS. This is not an official Jellyfin project or a reduced build of Jellyfin Server. The DSM package is named **NAS Media Folders**.

A lightweight folder media server for Jellyfin clients on resource-limited Synology NAS devices. Developer: **a.voronov**.

It imports the existing Synology Media Server ContentDirectory index and serves original media directly. Video, music and photo libraries follow your actual folder structure. The NAS does not run ffprobe, generate video covers, or transcode video.

- Application users with administrator/viewer roles, per-library access, independent favorites and playback positions.
- Settings UI at `/manage/`; start/stop through DSM Package Center.
- Folder playback, shuffle, serial next/previous controls and automatic continuation.
- Existing JPEG covers and native photo thumbnails; full-size photos in the viewer.
- Text subtitle extraction to WebVTT, cached separately from originals; limited by the installed ffmpeg codecs.
- Windows/PC tools for new-video JPEG covers and optional stream metadata.

This implements a tested subset of Jellyfin's API, not a drop-in replacement for every Jellyfin Server feature. Clients must support the original codecs. There is no video transcoding, online metadata scraping, remote access service, DSM account integration, or full Jellyfin administration dashboard.

## Install and migrate

Read [NAS setup](docs/nas-setup.md), [migration](docs/migration.md), and [PC cover generation](docs/pc-covers.md). The ARM64 SPK requires Synology Media Server and a configuration prepared using the release helper. Current experimental installations must migrate before old files are cleaned.

Use trusted LAN access. App library permissions apply to this app only; native DLNA has its own access rules. Credentials, user data, media and local machine configuration are not repository files.

## Develop and build

The maintained Go backend and embedded compatibility scripts are in `server/`; DSM packaging is in `packaging/synology/`. Follow [Go conventions](server/AGENTS.md).

Required: Go 1.24 or newer with the standard crypto/pbkdf2 package, Python 3.10+, and the pinned Jellyfin Web assets. Run `go test ./...` and `go vet ./...` from server/, or `server/check.ps1` with Go on PATH. All Go files must pass gofmt.

The exact supported Jellyfin Web archive is pinned by SHA256 in the asset preparation script; another build of the same version may have different minified bundles and will be refused. Supply the matching upstream source with release artifacts. See [development](docs/development.md) for commands.

No GitHub publication or credentials are required for local development. See [NOTICE](NOTICE.md) for component licenses.
