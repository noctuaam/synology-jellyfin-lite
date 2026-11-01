#!/bin/sh
# Add only the package account's read/list/traverse ACL; no chmod or recursive rewrite.
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
[ "$#" -eq 2 ] || { echo 'Usage: grant-media-read.sh SHARED_FOLDER BACKUP_DIRECTORY'; exit 1; }
folder=$1
backup=$2
case "$folder" in /volume[0-9]*/*) : ;; *) echo 'Expected a NAS shared folder'; exit 1 ;; esac
[ -d "$folder" ] && [ ! -L "$folder" ]
[ -d "$backup" ] && [ ! -L "$backup" ]
id MediaServer >/dev/null
acl=/usr/syno/bin/synoacltool
stamp=$(date +%Y%m%d-%H%M%S)
file="$backup/acl-$(basename "$folder")-$stamp.txt"
(set -C; "$acl" -get "$folder" > "$file")
chmod 600 "$file"
if grep -F 'user:MediaServer:allow:r-x---a-R-c--:fd--' "$file" >/dev/null; then
 echo 'The exact read-only ACL is already present.'
else
 "$acl" -add "$folder" 'user:MediaServer:allow:r-x---a-R-c--:fd--'
fi
sudo -u MediaServer test -r "$folder"
sudo -u MediaServer test -x "$folder"
echo "Read/list/traverse granted to MediaServer. ACL before change: $file"
