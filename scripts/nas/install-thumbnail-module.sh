#!/bin/sh
# Optional, version-locked native Synology DLNA photo thumbnail fix.
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
[ "$#" -eq 3 ] || { echo 'Usage: install-thumbnail-module.sh PATCHED_MODULE THUMBNAIL_HTTP_URL EXPECTED_NATIVE_JPEG'; exit 1; }
candidate=$1
url=$2
expected=$3
original=3e7775b3b9d0a20e79de963bd13cae255b87ed1db2ac7b0215c86d061b40f10c
patched=9a13be4b81292dee2fd74991e01b271ecea42d0ba15ab48c406b875d02fa4273
module=/var/packages/MediaServer/target/lib/lighttpd/mod_mediaserv.so
hash() { sha256sum "$1" | cut -d ' ' -f 1; }
[ "$(hash "$module")" != "$patched" ] || { echo 'Tested patch already installed; no changes'; exit 0; }
[ "$(hash "$module")" = "$original" ] || { echo 'Unsupported installed module'; exit 1; }
[ -f "$candidate" ] && [ ! -L "$candidate" ] && [ "$(hash "$candidate")" = "$patched" ]
[ -r "$expected" ]
case "$url" in http://127.0.0.1:50002/*|http://192.168.*:50002/*|http://10.*:50002/*) : ;; *) echo 'Expected local NAS thumbnail URL on port 50002'; exit 1 ;; esac
backup=/usr/local/lib/nas-media-folders-thumbnail-rollback
[ ! -e "$backup" ] || { echo 'Backup directory already exists'; exit 1; }
mkdir -m 700 "$backup"
cp -p "$module" "$backup/mod_mediaserv.so"
complete=0
cleanup() {
 if [ "$complete" -eq 0 ]; then
  cp -p "$backup/mod_mediaserv.so" "$module"
  synopkg restart MediaServer || true
  echo 'Original module restored.'
 fi
 rm -f "$backup/response.jpg" "$module.nmf-stage"
}
trap cleanup 0
trap 'exit 1' 1 2 15
cp -p "$module" "$module.nmf-stage"
cat "$candidate" > "$module.nmf-stage"
[ "$(hash "$module.nmf-stage")" = "$patched" ]
mv "$module.nmf-stage" "$module"
synopkg restart MediaServer
curl --fail --max-time 20 --silent --show-error "$url" -o "$backup/response.jpg"
[ "$(hash "$backup/response.jpg")" = "$(hash "$expected")" ]
complete=1
echo "PASS: native JPEG served exactly. Keep rollback: $backup"
echo 'After a MediaServer update recheck compatibility; never patch an unknown version.'
