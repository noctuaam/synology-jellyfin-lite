#!/bin/sh
# Only paths in a reviewed, exact manifest are eligible. Default is plan only.
set -eu
[ "$#" -ge 3 ] || { echo 'Usage: cleanup-experiments.sh MANIFEST BACKUP_DIRECTORY HEALTH_URL [--apply]'; exit 1; }
manifest=$1
backup=$2
health=$3
apply=${4:-}
[ -f "$manifest" ] && [ ! -L "$manifest" ] && [ -d "$backup" ] && [ ! -L "$backup" ]
[ -s /usr/local/etc/nas-media-folders/config.json ]
grep -F '/usr/local/lib/nas-jelly-lite' /usr/local/etc/nas-media-folders/config.json && { echo 'Active config still references old installation'; exit 1; }
grep -F '/usr/local/lib/nas-jelly-lite' /usr/local/lib/systemd/system/pkg-NASMediaFolders-server.service && exit 1
curl --fail --max-time 10 --silent "$health" | grep '"Version":"2.0.0"' >/dev/null || { echo 'Version 2.0.0 is not healthy'; exit 1; }
systemctl is-active --quiet nas-jelly-lite.service && { echo 'Legacy server is active'; exit 1; }
systemctl is-active --quiet nas-dlna-thumbnails.service && { echo 'Old thumbnail scan is active'; exit 1; }
data=$(/var/packages/NASMediaFolders/target/bin/nas-media-folders -config /usr/local/etc/nas-media-folders/config.json -print-data-directory)
while IFS= read -r path || [ -n "$path" ]; do
 [ -n "$path" ] || continue
 case "$path" in
 /volume[0-9]*/NetBackup/NAS-*-20[0-9]*) : ;;
 /usr/local/lib/nas-jelly-lite|/usr/local/lib/dlna-native-test.*|/usr/local/lib/dlna-fallback-test.*|/tmp/dlna-native-test.*|/tmp/dlna-debug.*) : ;;
 /etc/systemd/system/nas-jelly-lite.service|/etc/systemd/system/nas-jelly-lite-scan.service|/etc/systemd/system/nas-dlna-thumbnails.service|/etc/systemd/system/nas-dlna-thumbnails.timer|/usr/local/sbin/nas-dlna-thumbnails.sh) : ;;
 /volume[0-9]*/homes/*/test-mediaserver-native-thumbnails*.sh|/volume[0-9]*/homes/*/test-mediaserver-thumbnail-fallback.sh|/volume[0-9]*/homes/*/install-mediaserver-native-thumbnails*.sh|/volume[0-9]*/homes/*/install-dlna-thumbnail-compat.sh|/volume[0-9]*/homes/*/upgrade-dlna-thumbnails-medium.sh|/volume[0-9]*/homes/*/apply-nas-jelly-lite-*fix.sh|/volume[0-9]*/homes/*/mod_mediaserv-*.TEST-ONLY.so) : ;;
 *) echo "Not a recognized experiment: $path"; exit 1 ;;
 esac
 case "$path" in *'..'*|*'\'*|*' '*|*'	'*) echo 'Unsafe manifest path'; exit 1 ;; esac
 for configuration in /usr/local/etc/nas-media-folders/config.json "$data/server-settings.json"; do
  if [ -f "$configuration" ] && grep -F "$path" "$configuration" >/dev/null; then echo "Active settings reference this path: $path"; exit 1; fi
 done
 case "$backup/" in "$path/"*) echo "Backup must be outside cleanup targets"; exit 1 ;; esac
 [ ! -L "$path" ] || { echo "Symlink target refused: $path"; exit 1; }
 awk -v p="$path" '$5==p || index($5,p"/")==1 {f=1} END {exit !f}' /proc/self/mountinfo && { echo "Mounted path refused: $path"; exit 1; }
 [ ! -e "$path" ] || du -sh "$path"
done < "$manifest"
[ "$apply" = --apply ] || { echo 'Plan only; add --apply after testing the new version on clients.'; exit 0; }
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
umask 077
list=$(mktemp /tmp/nmf-cleanup.XXXXXX)
trap 'rm -f "$list"' 0
while IFS= read -r path || [ -n "$path" ]; do
 [ ! -e "$path" ] || printf '%s\n' "$path" >> "$list"
done < "$manifest"
archive="$backup/experiments-$(date +%Y%m%d-%H%M%S).tar.gz"
[ ! -e "$archive" ]
nice -n 19 tar -czpf "$archive" -T "$list"
gzip -t "$archive"
tar -tzf "$archive" > "$archive.contents.txt"
sha256sum "$archive" > "$archive.sha256"
systemctl disable nas-jelly-lite.service nas-dlna-thumbnails.timer 2>/dev/null || true
while IFS= read -r path; do
 # Every literal target was validated above and backed up successfully.
 rm -rf -- "$path"
done < "$list"
systemctl daemon-reload
echo "Archived and removed known experiments. Recovery archive: $archive"
echo 'Media originals, Data, photo mounts and native thumbnail fix retained.'
