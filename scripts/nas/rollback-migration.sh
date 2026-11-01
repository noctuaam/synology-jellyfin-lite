#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
[ "$#" -eq 1 ] || { echo 'Usage: rollback-migration.sh BACKUP_DIRECTORY'; exit 1; }
backup=$1
case "$backup" in /usr/local/etc/nas-media-folders/rollback-*) : ;; *) echo 'Unexpected backup path'; exit 1 ;; esac
[ -d "$backup" ] && [ ! -L "$backup" ]
[ "$(/bin/sqlite3 -readonly "$backup/catalog.db" 'PRAGMA quick_check;')" = ok ]
data=$(cat "$backup/data-directory")
old_config=$(cat "$backup/old-config-path")
case "$data" in /volume[0-9]*/*) : ;; *) exit 1 ;; esac
[ -f "$old_config" ] || { echo 'Restore the experiment archive first: the old configuration has been cleaned'; exit 1; }
[ ! -L "$data" ] && [ ! -L "$data/catalog.db" ]
systemctl stop pkg-NASMediaFolders-server.service
saved="$backup/state-before-rollback-$(date +%Y%m%d-%H%M%S)"
mkdir -m 700 "$saved"
for name in catalog.db catalog.db-wal catalog.db-shm credentials.json server-id server-settings.json; do
 [ ! -e "$data/$name" ] || mv "$data/$name" "$saved/$name"
done
for name in config.json video-metadata.jsonl; do
 [ ! -e "/usr/local/etc/nas-media-folders/$name" ] || mv "/usr/local/etc/nas-media-folders/$name" "$saved/standalone-$name"
done
cp "$backup/catalog.db" "$data/catalog.db"
cp "$backup/credentials.json" "$data/credentials.json"
chown MediaServer:MediaServer "$data/catalog.db" "$data/credentials.json"
chmod 600 "$data/catalog.db" "$data/credentials.json"
cp -p "$backup/nas-media-folders" /var/packages/NASMediaFolders/target/bin/nas-media-folders
cp -p "$backup/package-scripts/"* /var/packages/NASMediaFolders/scripts/
cp -p "$backup/server.service" /usr/local/lib/systemd/system/pkg-NASMediaFolders-server.service
# Package extraction can change code directory modes even before start fails.
package=$(readlink -f /var/packages/NASMediaFolders/target)
case "$package" in /volume[0-9]*/@appstore/NASMediaFolders) : ;; *) exit 1 ;; esac
find "$package" -type d -exec chmod 755 '{}' ';'
chmod 755 "$package/bin/nas-media-folders"
sudo -u MediaServer test -x "$package/bin/nas-media-folders"
systemctl daemon-reload
systemctl reset-failed pkg-NASMediaFolders-server.service
systemctl start pkg-NASMediaFolders-server.service
sleep 2
systemctl is-active --quiet pkg-NASMediaFolders-server.service
echo "Previous server and snapshot restored. New-version state retained at: $saved"
echo 'If package replacement had already begun, check its displayed version in DSM.'
