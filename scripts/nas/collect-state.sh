#!/bin/sh
# Read-only inventory. No service is stopped and no index is rebuilt.
set -eu
output=${1:-.}
[ -d "$output" ] && [ ! -L "$output" ] || { echo 'Pass an existing report directory'; exit 1; }
umask 027
report=$(mktemp "$output/nas-state-$(date +%Y%m%d-%H%M%S).XXXXXX.txt")
{
    echo '=== Platform ==='
    date
    uname -m
    cat /etc.defaults/VERSION 2>/dev/null || true
    echo '=== Installed package metadata ==='
    for package in NASMediaFolders MediaServer; do
        echo "$package"
        grep -E '^(package|version|arch)=' "/var/packages/$package/INFO" 2>/dev/null || true
    done
    echo '=== Relevant services ==='
    for unit in pkg-NASMediaFolders-server.service pkgctl-NASMediaFolders.service nas-jelly-lite.service nas-jelly-lite-scan.service nas-dlna-photos.service nas-dlna-thumbnails.service nas-dlna-thumbnails.timer; do
        systemctl is-active "$unit" 2>/dev/null || true
        systemctl is-enabled "$unit" 2>/dev/null || true
        systemctl cat "$unit" 2>/dev/null || true
    done
    echo '=== Application and rollback sizes ==='
    for directory in /usr/local/lib/nas-jelly-lite /usr/local/lib/nas-dlna-thumbnail-fix-v2 /usr/local/etc/nas-media-folders /volume*/@nas-jelly-lite /volume*/@nas-media-folders; do
        [ ! -d "$directory" ] || du -sk "$directory"
    done
    echo '=== Legacy application files (names only) ==='
    if [ -d /usr/local/lib/nas-jelly-lite ]; then
        find /usr/local/lib/nas-jelly-lite -maxdepth 1 -type f -exec ls -lh '{}' ';'
    fi
    echo '=== Native module checksums ==='
    for file in /var/packages/MediaServer/target/lib/lighttpd/mod_mediaserv.so /var/packages/NASMediaFolders/target/bin/nas-media-folders; do
        [ ! -f "$file" ] || sha256sum "$file"
    done
    echo '=== Read-only photo mount script ==='
    if [ -f /usr/local/sbin/nas-dlna-photos.sh ]; then
        cat /usr/local/sbin/nas-dlna-photos.sh
    fi
    echo '=== Photo bind mounts ==='
    awk '$5 ~ /\/photo\/Users\// {print}' /proc/self/mountinfo
    echo '=== Temporary test directories ==='
    for directory in /tmp/dlna-native-test.* /tmp/dlna-fallback-test.* /tmp/dlna-debug.* /usr/local/lib/dlna-native-test.* /usr/local/lib/dlna-fallback-test.*; do
        [ ! -d "$directory" ] || du -sk "$directory"
    done
    echo '=== Temporary test files in DSM homes (depth 2 only) ==='
    for directory in /volume*/homes; do
        [ ! -d "$directory" ] || find "$directory" -maxdepth 2 -type f \( -name 'test-mediaserver*.sh' -o -name 'mod_mediaserv-*.so' -o -name 'install-dlna*.sh' -o -name 'install-mediaserver*.sh' -o -name 'apply-nas-jelly-lite*.sh' -o -name 'upgrade-dlna*.sh' \) -exec ls -lh '{}' ';'
    done
    echo '=== Current server health ==='
    curl --noproxy '*' --connect-timeout 3 --max-time 8 -s http://127.0.0.1:8097/health || true
    echo
    echo 'Inventory complete. Configuration credentials and media contents were not collected.'
} > "$report" 2>&1
chgrp administrators "$report" 2>/dev/null || true
chmod 640 "$report"
printf 'Report: %s\n' "$report"
