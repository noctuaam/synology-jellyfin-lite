# Contributing

Use small, readable Go functions and gofmt; run go vet and meaningful regression tests. Keep protocol identifiers and existing state stable across upgrades. Do not reformat upstream minified assets. Compatibility patches must check their exact input hashes and fail clearly for unsupported builds.

Test changes against a separate data directory and synthetic media before using personal libraries. Preserve originals and native Synology caches. Never commit credentials, LAN addresses, home usernames, generated catalogs, logs, proprietary modules or local release binaries. New configuration must have documented defaults and reversible migration.

When changing the client, preserve ES5 compatibility for the old webOS photo/video helpers. Test natural folder ordering, queue boundaries, original photo viewing, subtitles, image versus stream endpoints, range requests and user isolation where relevant.
