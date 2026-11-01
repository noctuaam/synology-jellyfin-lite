#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
[ "$#" -eq 2 ] || { echo 'Usage: setup-package.sh RELEASE_BINARY CONFIG_JSON'; exit 1; }
binary=$1
configuration=$2
[ -f "$binary" ] && [ ! -L "$binary" ] && [ -f "$configuration" ] && [ ! -L "$configuration" ]
id MediaServer >/dev/null
[ ! -e /usr/local/etc/nas-media-folders/config.json ] || { echo 'Configuration already exists; use migration or update'; exit 1; }
chmod 755 "$binary"
data=$("$binary" -config "$configuration" -print-data-directory)
case "$data" in /volume[0-9]*/@nas-media-folders) : ;; *) echo 'Fresh setup requires a dedicated /volumeN/@nas-media-folders state directory'; exit 1 ;; esac
[ ! -L "$data" ]
[ ! -e "$data/credentials.json" ] && [ ! -e "$data/catalog.db" ]
mkdir -p /usr/local/etc/nas-media-folders "$data"
chmod 755 /usr/local/etc/nas-media-folders
chown MediaServer:MediaServer "$data"
chmod 700 "$data"
cp "$configuration" /usr/local/etc/nas-media-folders/config.json
chown root:MediaServer /usr/local/etc/nas-media-folders/config.json
chmod 640 /usr/local/etc/nas-media-folders/config.json
runtime=$(mktemp -d /usr/local/lib/nmf-setup.XXXXXX)
chmod 755 "$runtime"
cp "$binary" "$runtime/server"
chmod 755 "$runtime/server"
trap 'rm -f "$runtime/server"; rmdir "$runtime"' 0
sudo -u MediaServer "$runtime/server" -config /usr/local/etc/nas-media-folders/config.json -init -username admin
controller=/etc/systemd/system/pkgctl-NASMediaFolders.service.d
mkdir -p "$controller"
cat > "$controller/nas-media-folders.conf" <<'UNIT'
[Unit]
Wants=pkg-NASMediaFolders-server.service
After=pkg-NASMediaFolders-server.service
UNIT
systemctl daemon-reload
echo 'Save the generated administrator password. Install the SPK through DSM now.'
