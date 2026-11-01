#!/bin/sh
# Only selected Photos folders are exposed. Originals are never copied.
set -eu
config=${NMF_PHOTO_CONFIG:-/usr/local/etc/nas-media-folders/photos}
[ "$(id -u)" -eq 0 ] || { echo 'Run as root'; exit 1; }
homes=$(cat "$config/homes-root")
photo=$(cat "$config/photo-root")
base="$photo/Users"
for root in "$homes" "$photo"; do
    case "$root" in /*) : ;; *) echo 'Roots must be absolute'; exit 2 ;; esac
    case "$root" in *'\'*|*'
'*) echo 'Unsupported root path'; exit 2 ;; esac
done
attempt=0
while [ ! -d "$homes" ] || [ ! -d "$photo" ]; do
    attempt=$((attempt + 1))
    [ "$attempt" -le 60 ] || { echo 'Photo volumes not ready after five minutes'; exit 3; }
    sleep 5
done
[ ! -L "$homes" ] && [ ! -L "$photo" ] && [ ! -L "$base" ]
mkdir -p "$base"
created=$(mktemp /tmp/nmf-photo-mounts.XXXXXX)
complete=0
cleanup() {
    if [ "$complete" -eq 0 ]; then
        while IFS= read -r destination; do
            [ -n "$destination" ] && /bin/umount "$destination" || true
        done < "$created"
    fi
    rm -f "$created"
}
trap cleanup 0
trap 'exit 1' 1 2 15
mounted() {
    awk -v target="$1" '
      { p=$5; gsub(/\\040/, " ", p); gsub(/\\011/, "\t", p) }
      p==target { found=1; if ($6 ~ /(^|,)ro(,|$)/) readonly=1 }
      END { if (!found) exit 1; if (!readonly) exit 2 }
    ' /proc/self/mountinfo
}
while IFS= read -r user || [ -n "$user" ]; do
    case "$user" in ''|'#'*) continue ;; .|..|*/*|*'\'*|*'..'*) echo 'Invalid username in users file'; exit 4 ;; esac
    [ ! -L "$homes/$user" ] || { echo "User directory is a symlink: $user"; exit 1; }
 source="$homes/$user/Photos"
    destination="$base/$user"
    [ -d "$source" ] && [ ! -L "$source" ] && [ ! -L "$destination" ] || {
        echo "Missing or unsafe Photos folder: $user"; exit 5;
    }
    mkdir -p "$destination"
    if mounted "$destination"; then
        :
    else
        code=$?
        [ "$code" -eq 1 ] || { echo "Existing mount is writable: $destination"; exit 6; }
        [ -z "$(ls -A "$destination")" ] || { echo "Destination is not empty: $destination"; exit 7; }
        /bin/mount --bind "$source" "$destination"
        printf '%s\n' "$destination" >> "$created"
        /bin/mount -o remount,bind,ro "$source" "$destination"
    fi
    mounted "$destination"
    [ "$(stat -c '%d:%i' "$source")" = "$(stat -c '%d:%i' "$destination")" ] || {
        echo "Mount belongs to a different source: $destination"; exit 8;
    }
    printf 'Read-only: %s -> %s\n' "$source" "$destination"
done < "$config/users"
complete=1
if [ -x /usr/syno/bin/synoindex ]; then
    while IFS= read -r destination; do
        /usr/syno/bin/synoindex -A "$destination" || echo "Index request failed: $destination"
    done < "$created"
fi
