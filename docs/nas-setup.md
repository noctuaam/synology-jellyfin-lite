# Synology setup from scratch

Target: DSM 7, ARM64 (tested hardware DS220j). Install Synology **Media Server**, enable the user home service if backing up phone photos, and create media shared folders through DSM. Configure Media Indexing for video, music and photo. Let the initial Synology indexing finish. This app imports that index, with no NAS video probing or conversion.

## Photos kept in user homes

Phones can continue backing up to `homes/<name>/Photos`. The mount helper exposes selected folders as `photo/Users/<name>` using read-only bind mounts. There is one set of originals, no photo copying. A read-only bind does not change the source folder's write permissions for phone backups.

1. Create a UTF-8 **LF** users file, one DSM username per line; select only users whose photos should be visible through DLNA. See `examples/photo-users.example.txt`.
2. Run the plan: `sudo sh scripts/nas/install-photo-mounts.sh --homes-root /volume1/homes --photo-root /volume1/photo --users-file /absolute/path/users.txt`.
3. Review the printed sources/destinations; repeat with `--apply` to enable `nas-dlna-photos.service` at boot.
4. Grant the **MediaServer** system account read-only access in DSM's shared-folder permissions. The optional `grant-media-read.sh SHARED_FOLDER BACKUP_DIRECTORY` adds one read/list/traverse ACL and saves the previous ACL listing. It does not recursively replace permissions. Verify access on a sample original and `@eaDir` thumbnail with `sudo -u MediaServer head -c 16 '/absolute/path/file' >/dev/null`.
5. Add `photo` to Synology's image indexing if not already configured. Do not add the original homes folders again, which could duplicate entries.

Check `systemctl is-enabled nas-dlna-photos.service`, `systemctl status nas-dlna-photos.service --no-pager` and `/proc/self/mountinfo`: destinations must show `ro`. The source homes remain writable. Existing foreign/writable mounts, nonempty destinations and symbolic-link roots are refused. On error, only mounts newly created in that run are rolled back.

All users selected here become visible to devices allowed by Synology DLNA. DLNA does not enforce this app's separate users or library permissions. Keep native DLNA limited to your trusted LAN; router port forwarding, UPnP and VPN routing are separate settings.

## Thumbnail compatibility

Native Synology thumbnails belong under each file's `@eaDir` folder. Do not delete these caches as experimental files. On Media Server 2.2.2-3412 ARM64, the photo endpoint in our installation looked for `SYNOPHOTO_THUMB_S.jpg` while existing caches were `SYNOFILE_THUMB_M.jpg`/`_S.jpg`. The tested native module fix prefers medium then small without generating duplicate files. That fix is version-specific and independent of this application's package; retain its rollback when upgrading. Never install a module built for another architecture or hash.

## Resource limits

The package runs as MediaServer, GOMAXPROCS=1, Nice=19, CPU quota 50%, memory cap 128 MiB. Direct play requires a codec the client can decode. DS220j should not transcode video. The PC cover script handles HEVC decoding and frame generation on the computer instead.

## Fresh application installation

Copy `examples/config.example.json` to your private configuration file, select the volume and listener, and run `sudo sh scripts/nas/setup-package.sh /absolute/path/nas-media-folders-arm64 /absolute/path/config.json`. Save the generated administrator password, then install the SPK in DSM. Give MediaServer read-only access to selected media shares. Wait for the index import before expecting the library to appear. Connection URLs and root identifiers can be adjusted in `/manage/`; changes apply after restarting the package.

Optional native thumbnail patch: copy the original module to the PC, run `python scripts/pc/patch_thumbnail_module.py original.so patched.so`, then copy the candidate to the NAS. Run `sudo sh scripts/nas/install-thumbnail-module.sh /absolute/path/patched.so 'http://NAS_LAN_IP:50002/sp/JPEG_TN/ACTUAL_ID.jpg' '/absolute/path/existing/SYNOFILE_THUMB_M.jpg'` with a matching sample's real index ID and cache file. The installer accepts only exact tested checksums, verifies the JPEG byte-for-byte and restores the original on failure. Do not guess IDs or reuse an ID from another catalog.
