"""Extract the exact supported upstream Web build without executing package scripts."""
import argparse
import hashlib
import io
from pathlib import Path
import tarfile

ARCHIVE_SHA256 = '303bf4bf0892a422b75627c158f6177464ab216d0a431225277ce3fbffbbb08d'
MAIN_SHA256 = '8774a41779f20b4d4ed056434fa0d4630ce7b28e908522e5210a34ab67137672'


def prepare(archive, destination):
    raw = archive.read_bytes()
    if hashlib.sha256(raw).hexdigest() != ARCHIVE_SHA256 or raw[:8] != b'!<arch>\n':
        raise ValueError('Unsupported Jellyfin Web archive; exact pinned build required')
    if destination.exists():
        raise ValueError('Destination already exists; choose a new empty output path')
    destination.mkdir(parents=True)
    position = 8
    extracted = 0
    while position < len(raw):
        header = raw[position:position+60]
        size = int(header[48:58])
        name = header[:16].decode().strip().rstrip('/')
        if name.startswith('data.tar'):
            with tarfile.open(fileobj=io.BytesIO(raw[position+60:position+60+size])) as contents:
                for member in contents:
                    name = member.name.removeprefix('./')
                    prefix = 'usr/share/jellyfin/web/'
                    if not name.startswith(prefix) or not member.isfile():
                        continue
                    target = destination / name[len(prefix):]
                    if not target.resolve().is_relative_to(destination.resolve()):
                        raise ValueError('Unsafe archive path')
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(contents.extractfile(member).read())
                    extracted += 1
        position += 60 + size + size % 2
    if hashlib.sha256((destination/'main.jellyfin.bundle.js').read_bytes()).hexdigest() != MAIN_SHA256:
        raise ValueError('Unexpected main bundle')
    print('PASS: pinned upstream assets extracted:', extracted)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    prepare(args.archive, args.destination)
