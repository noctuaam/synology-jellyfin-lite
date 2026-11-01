# Migration from the experimental single-user installation

Do not uninstall the old DSM package first. Stop downloads or other administrative changes while upgrading; media playback should be closed.

1. Copy the release bundle to a shared folder on the NAS. Verify `SHA256SUMS` (LF line endings).
2. Run `sudo sh scripts/nas/prepare-migration.sh /absolute/path/nas-media-folders-arm64 /absolute/path/old-config.json`.
3. Install the new SPK as an update in DSM Package Center.
4. Open `/manage/`, sign in with the existing application account, and verify favorites, progress, original photos, music, subtitles and serial next/previous playback on your clients.
5. Create additional application users if needed. The migrated account is the first administrator. App accounts are independent of DSM.
6. Only after verification use the reviewed cleanup plan. Keep the rollback bundle and the `Data/backups/before-multiuser-*` snapshot.

The existing Data directory is retained. Media originals, native photo thumbnails and the Synology index are not moved. Legacy media/user/server identifiers are retained. Existing favorites and progress remain under their original keys; new users have separate state. Existing owner sessions remain valid unless the administrator resets that account's password or disables it.

Configuration is moved to `/usr/local/etc/nas-media-folders/config.json`; an externally referenced video metadata file is copied alongside it. Changes made through the admin UI are stored in `Data/server-settings.json` and apply after restart. Do not delete Data on package uninstall.

## Rollback boundaries

Before account migration the server writes a consistent SQLite snapshot using `VACUUM INTO`, including committed WAL contents, plus hashed credentials and configuration. Protect backups as credentials. The NAS migration helper separately keeps the prior binary, unit and config.

A binary rollback normally can read the existing owner state because legacy rows are preserved. For an exact rollback, stop the app, restore the backed-up binary/config/unit and the matching catalog snapshot, remove only the stopped database's `-wal`/`-shm`, then restart. This intentionally discards changes since that snapshot; never restore a catalog while the service is running. Keep a copy of the new data before restoring.

The optional Synology native thumbnail fix and `nas-dlna-photos.service` are independent and must remain installed.
