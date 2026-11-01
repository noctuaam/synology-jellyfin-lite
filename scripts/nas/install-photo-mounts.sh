#!/bin/sh
set -eu
homes= photo= users= apply=0
usage() {
    echo 'Usage: install-photo-mounts.sh --homes-root /volumeN/homes --photo-root /volumeN/photo --users-file /path/users.txt [--apply]'
}
while [ "$#" -gt 0 ]; do
    case "$1" in
        --homes-root) homes=$2; shift 2 ;;
        --photo-root) photo=$2; shift 2 ;;
        --users-file) users=$2; shift 2 ;;
        --apply) apply=1; shift ;;
        *) usage; exit 1 ;;
    esac
done
[ -n "$homes" ] && [ -n "$photo" ] && [ -f "$users" ] || { usage; exit 1; }
case "$homes:$photo" in *'\'*|*'
'*) echo 'Unsupported root path'; exit 1 ;; esac
for root in "$homes" "$photo"; do
    case "$root" in /*) : ;; *) echo 'Roots must be absolute'; exit 1 ;; esac
    [ -d "$root" ] && [ ! -L "$root" ] || { echo "Root missing or symlinked: $root"; exit 1; }
done
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ -f "$here/mount-photos.sh" ]
count=0
while IFS= read -r user || [ -n "$user" ]; do
    case "$user" in ''|'#'*) continue ;; .|..|*/*|*'\'*|*'..'*) echo 'Invalid username'; exit 1 ;; esac
    [ ! -L "$homes/$user" ] || { echo "User directory is a symlink: $user"; exit 1; }
    [ -d "$homes/$user/Photos" ] && [ ! -L "$homes/$user/Photos" ] || { echo "Photos missing: $user"; exit 1; }
    printf 'Will expose read-only: %s/%s/Photos -> %s/Users/%s\n' "$homes" "$user" "$photo" "$user"
    count=$((count + 1))
done < "$users"
[ "$count" -gt 0 ] || { echo 'No users selected'; exit 1; }
[ "$apply" -eq 1 ] || { echo 'Plan only. Add --apply to install.'; exit 0; }
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo'; exit 1; }
config=/usr/local/etc/nas-media-folders/photos
[ ! -L "$config" ] && [ ! -L /usr/local/sbin/nas-dlna-photos.sh ]
mkdir -p "$config/backups"
chmod 700 "$config" "$config/backups"
stamp=$(date +%Y%m%d-%H%M%S)
backup="$config/backups/$stamp"
mkdir "$backup"
for file in /usr/local/sbin/nas-dlna-photos.sh /etc/systemd/system/nas-dlna-photos.service; do
    [ ! -f "$file" ] || cp -p "$file" "$backup/$(basename "$file")"
done
for file in homes-root photo-root users; do
    [ ! -f "$config/$file" ] || cp -p "$config/$file" "$backup/$file"
done
printf '%s\n' "$homes" > "$config/homes-root"
printf '%s\n' "$photo" > "$config/photo-root"
cp "$users" "$config/users"
chmod 600 "$config/homes-root" "$config/photo-root" "$config/users"
cp "$here/mount-photos.sh" /usr/local/sbin/nas-dlna-photos.sh
chmod 700 /usr/local/sbin/nas-dlna-photos.sh
cat > /etc/systemd/system/nas-dlna-photos.service <<'UNIT'
[Unit]
Description=Read-only user Photos folders for DLNA
After=local-fs.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/nas-dlna-photos.sh
RemainAfterExit=yes
TimeoutStartSec=360

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable nas-dlna-photos.service
systemctl restart nas-dlna-photos.service
systemctl status nas-dlna-photos.service --no-pager
echo "Previous files: $backup"
