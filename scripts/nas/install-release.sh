#!/bin/sh
# Upgrade the experimental DSM package, with a consistent snapshot and rollback.
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
here=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
old_config=${1:-/usr/local/lib/nas-jelly-lite/dsm-config.json}
[ -s "$here/nas-media-folders-arm64" ] && [ -s "$here/NASMediaFolders-armv8-2.0.0-0021.spk" ]
(cd "$here" && sha256sum -c SHA256SUMS)
[ ! -L /usr/local/lib/nas-media-folders-upgrade ]
[ ! -e /usr/local/lib/nas-media-folders-upgrade ] || { echo 'Previous upgrade staging directory exists'; exit 1; }
mkdir -m 755 /usr/local/lib/nas-media-folders-upgrade
cp "$here/nas-media-folders-arm64" /usr/local/lib/nas-media-folders-upgrade/server
chmod 755 /usr/local/lib/nas-media-folders-upgrade/server
trap 'rm -f /usr/local/lib/nas-media-folders-upgrade/server; rmdir /usr/local/lib/nas-media-folders-upgrade 2>/dev/null || true' 0
sh "$here/scripts/nas/prepare-migration.sh" /usr/local/lib/nas-media-folders-upgrade/server "$old_config"
backup=$(cat /usr/local/etc/nas-media-folders/last-migration-backup)
installed=0
recover() {
 code=$?
 if [ "$installed" -eq 0 ]; then
  echo 'Upgrade failed; restoring the previous server and catalog.'
  sh "$here/scripts/nas/rollback-migration.sh" "$backup" || echo "Rollback needs attention: $backup"
 fi
 rm -f /usr/local/lib/nas-media-folders-upgrade/server
 rmdir /usr/local/lib/nas-media-folders-upgrade 2>/dev/null || true
 exit "$code"
}
trap recover 0
# Offline verification also performs the guarded account migration. The old
# server is stopped, so no two writers can alter the database concurrently.
sudo -u MediaServer /usr/local/lib/nas-media-folders-upgrade/server -config /usr/local/etc/nas-media-folders/config.json -verify
systemctl reset-failed pkg-NASMediaFolders-server.service pkgctl-NASMediaFolders.service || true
synopkg install "$here/NASMediaFolders-armv8-2.0.0-0021.spk"
sleep 2
systemctl is-active --quiet pkg-NASMediaFolders-server.service
installed=1
echo 'PASS: 2.0.0 installed and running. Existing credentials, catalog and playback state retained.'
echo "Rollback: sudo sh $here/scripts/nas/rollback-migration.sh $backup"
echo 'Check TV playback and user settings before running cleanup. Cleanup has NOT run.'
