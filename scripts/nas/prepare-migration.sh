#!/bin/sh
# Prepare a stopped, reversible upgrade; install the SPK through DSM afterwards.
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
[ "$#" -eq 2 ] || { echo 'Usage: prepare-migration.sh NEW_BINARY OLD_CONFIG'; exit 1; }
binary=$1
old_config=$2
[ -f "$binary" ] && [ ! -L "$binary" ] && [ -f "$old_config" ] && [ ! -L "$old_config" ]
[ ! -e /usr/local/etc/nas-media-folders/config.json ] || { echo 'Configuration already migrated; stopping'; exit 1; }
# This migration supports the existing single-user installation. The config
# preparer preserves its Data path, so SQLite itself creates the WAL-safe
# account migration snapshot on first start of the new binary.
[ ! -e /usr/local/etc/nas-media-folders/video-metadata.jsonl ] || { echo 'A standalone metadata file already exists; inspect the previous attempt'; exit 1; }
mkdir -p /usr/local/etc/nas-media-folders
chmod 755 /usr/local/etc/nas-media-folders
stamp=$(date +%Y%m%d-%H%M%S)
backup="/usr/local/etc/nas-media-folders/rollback-$stamp-$$"
mkdir -p "$backup"
chmod 700 "$backup"
cp -p "$old_config" "$backup/config.json"
cp -p /var/packages/NASMediaFolders/target/bin/nas-media-folders "$backup/nas-media-folders"
mkdir "$backup/package-scripts"
cp -p /var/packages/NASMediaFolders/scripts/* "$backup/package-scripts/"
cp -p /var/packages/NASMediaFolders/INFO "$backup/package-INFO"
cp -p /usr/local/lib/systemd/system/pkg-NASMediaFolders-server.service "$backup/server.service"
systemctl is-active --quiet pkg-NASMediaFolders-server.service && was_active=1 || was_active=0
printf '%s\n' "$was_active" > "$backup/was-active"
rollback_on_error() {
 result=$?
 if [ "$result" -ne 0 ]; then
  echo "Preparation failed; restoring previous service. Backup: $backup"
  rm -f /usr/local/etc/nas-media-folders/config.json /usr/local/etc/nas-media-folders/video-metadata.jsonl
  [ "$was_active" -eq 0 ] || systemctl start pkg-NASMediaFolders-server.service || true
 fi
}
trap rollback_on_error 0
systemctl stop pkg-NASMediaFolders-server.service
systemctl stop nas-jelly-lite.service 2>/dev/null || true
chmod 755 "$binary"
data=$("$binary" -config "$old_config" -print-data-directory)
case "$data" in /volume[0-9]*/*) : ;; *) echo 'Unexpected data directory'; exit 1 ;; esac
[ -s "$data/catalog.db" ] && [ -s "$data/credentials.json" ]
printf '%s\n' "$data" > "$backup/data-directory"
printf '%s\n' "$old_config" > "$backup/old-config-path"
/bin/sqlite3 -readonly "$data/catalog.db" "VACUUM INTO '$backup/catalog.db';"
[ "$(/bin/sqlite3 -readonly "$backup/catalog.db" 'PRAGMA quick_check;')" = ok ]
cp -p "$data/credentials.json" "$backup/credentials.json"
chmod 600 "$backup/catalog.db" "$backup/credentials.json"

chmod 755 "$binary"
"$binary" -config "$old_config" -prepare-package-config /usr/local/etc/nas-media-folders/config.json
chown root:MediaServer /usr/local/etc/nas-media-folders/config.json
chmod 640 /usr/local/etc/nas-media-folders/config.json
if [ -f /usr/local/etc/nas-media-folders/video-metadata.jsonl ]; then
 chown root:MediaServer /usr/local/etc/nas-media-folders/video-metadata.jsonl
 chmod 640 /usr/local/etc/nas-media-folders/video-metadata.jsonl
fi
# DSM's controller starts/stops this dependency; lifecycle scripts stay unprivileged.
controller=/etc/systemd/system/pkgctl-NASMediaFolders.service.d
mkdir -p "$controller"
cat > "$controller/nas-media-folders.conf" <<'UNIT'
[Unit]
Wants=pkg-NASMediaFolders-server.service
After=pkg-NASMediaFolders-server.service
UNIT
systemctl disable nas-jelly-lite.service 2>/dev/null || true
systemctl daemon-reload
printf '%s\n' "$backup" > /usr/local/etc/nas-media-folders/last-migration-backup
trap - 0
printf 'Prepared. Install the new SPK in DSM. Keep rollback directory: %s\n' "$backup"
echo 'Do not clean old installations until the new version and TV playback are verified.'
