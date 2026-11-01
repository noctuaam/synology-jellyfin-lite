# Cleaning experiments after migration

Do not delete `/usr/local/lib/nas-jelly-lite` while the installed unit or configuration references it. The migration moves active configuration and video metadata outside that directory, retaining the existing Data directory.

Use an **exact, reviewed manifest**, one absolute path per line. `cleanup-experiments.sh MANIFEST BACKUP_DIRECTORY HEALTH_URL` prints a plan. The script requires a healthy 2.0.0 server, checks that the old server/thumbnail scan is stopped, rejects unknown path classes, symlinks and mounted destinations. Add `--apply` only after clients have been verified.

Before deleting anything it writes and verifies a compressed recovery archive plus its file list and SHA256. Only paths in the validated manifest are removed. Keep this archive with the migration snapshot. To undo cleanup, stop the application and restore selected old paths from the archive; inspect its listing first. Root archives contain sensitive configuration and must remain private.

Never include media originals, `@eaDir`, Synology indexing databases, the active Data directory, `nas-dlna-photos.service`, or the native module fix rollback. Some `SYNOPHOTO_*` caches are legitimate Synology Photos files and cannot be classified as our experiments by filename alone.

If the native thumbnail module rollback re-enables the old compatibility timer,
keep that disabled timer, its service and executable script. They are a rollback
dependency, not a running background scan. The personal cleanup manifest retains
them; their size is negligible. Do not remove them independently of that rollback.
