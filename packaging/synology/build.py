"""Build the ARM64 DSM package using only the Python standard library."""
from pathlib import Path, PurePosixPath
import argparse, gzip, hashlib, io, json, struct, tarfile, zlib

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--output', type=Path, required=True)
parser.add_argument('--binary', type=Path, required=True)
parser.add_argument('--web', type=Path, required=True)
parser.add_argument('--source', type=Path, default=ROOT / 'server')
parser.add_argument('--version', default='2.0.0-0021')
parser.add_argument('--sha256')
args = parser.parse_args()
OUT = args.output
OUT.mkdir(parents=True, exist_ok=True)
BINARY = args.binary
if args.sha256:
    assert hashlib.sha256(BINARY.read_bytes()).hexdigest() == args.sha256
assert BINARY.read_bytes()[:4] == b'\x7fELF'
assert struct.unpack_from('<H', BINARY.read_bytes(), 18)[0] == 183

def png(size):
    asset = HERE / 'assets' / f'icon_{size}.png'
    if asset.is_file():
        return asset.read_bytes()
    # Original simple folder/play icon, drawn without external image assets.
    pixels = bytearray()
    for y in range(size):
        pixels.append(0)
        for x in range(size):
            a, b = x / size, y / size
            color = (24, 38, 62, 255)
            if .13 < a < .87 and .32 < b < .79 or .13 < a < .46 and .22 < b < .38:
                color = (67, 158, 235, 255)
            if .44 < a < .68 and abs(b - .55) < (.68 - a) * .65:
                color = (255, 255, 255, 255)
            pixels.extend(color)
    def chunk(kind, data):
        return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', size, size, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(pixels)) + chunk(b'IEND', b'')

def tar_bytes(files, compressed=False):
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        directories = {str(parent) for name in files for parent in PurePosixPath(name).parents if str(parent) != '.'}
        # DSM extracts with a restrictive umask: implicit directories become 0700.
        for name in sorted(directories, key=lambda value: (value.count('/'), value)):
            entry = tarfile.TarInfo(name)
            entry.type, entry.mode, entry.mtime = tarfile.DIRTYPE, 0o755, 0
            archive.addfile(entry)
        for name, (data, mode) in sorted(files.items()):
            entry = tarfile.TarInfo(name)
            entry.size, entry.mode, entry.mtime = len(data), mode, 0
            entry.uid = entry.gid = 0
            archive.addfile(entry, io.BytesIO(data))
    return gzip.compress(raw.getvalue(), mtime=0) if compressed else raw.getvalue()

payload = {'bin/nas-media-folders': (BINARY.read_bytes(), 0o755)}
for name in ['LICENSE', 'NOTICE.md']:
    payload['licenses/' + name] = ((ROOT / name).read_bytes(), 0o644)
for path in args.web.rglob('*'):
    if path.is_file():
        payload['web/' + path.relative_to(args.web).as_posix()] = (path.read_bytes(), 0o644)
for path in args.source.glob('*'):
    if path.is_file():
        payload['source/' + path.name] = (path.read_bytes(), 0o644)
licenses = ROOT / 'third_party/licenses'
for path in licenses.rglob('*'):
    if path.is_file():
        payload['licenses/' + path.relative_to(licenses).as_posix()] = (path.read_bytes(), 0o644)
payload['web/nas-media-icon.png'] = ((HERE / 'assets/nas-media-folders-master.png').read_bytes(), 0o644)
payload['ui/config'] = (json.dumps({'.url': {'local.nas.MediaFolders': {
    'type': 'url', 'title': 'NAS Media Folders', 'desc': 'Видео, музыка и фотографии',
    'icon': 'images/nas_media_v2_{0}.png', 'url': '/webman/3rdparty/NASMediaFolders/index.html', 'allUsers': False
}}}, ensure_ascii=False).encode(), 0o644)
payload['ui/index.html'] = (b'<!doctype html><meta charset="utf-8"><title>NAS Media Folders</title><script>location.replace("http://"+location.hostname+":8097/manage/");</script>', 0o644)
for size in [16, 24, 32, 48, 64, 72, 256]:
    payload[f'ui/images/nas_media_v2_{size}.png'] = (png(size), 0o644)
package_tgz = tar_bytes(payload, compressed=True)
info = {
    'package': 'NASMediaFolders', 'displayname': 'NAS Media Folders',
    'version': args.version, 'arch': 'armv8', 'os_min_ver': '7.0-40000',
    'description': 'Lightweight folder media server for Jellyfin clients. Uses the existing Synology Media Server index. Migrates the existing personal installation.',
    'description_rus': 'Лёгкий сервер папок для клиентов Jellyfin. Использует индекс Synology Media Server. Сохраняет текущую медиатеку и настройки.',
    'maintainer': 'a.voronov', 'thirdparty': 'yes', 'beta': 'yes',
    'install_dep_packages': 'MediaServer', 'start_dep_services': 'network.target',
    'ctl_stop': 'yes', 'ctl_uninstall': 'yes', 'precheckstartstop': 'yes',
    'adminport': '8097', 'adminprotocol': 'http', 'adminurl': 'manage/', 'checkport': 'no',
    'dsmuidir': 'ui', 'dsmappname': 'local.nas.MediaFolders',
    'checksum': hashlib.md5(package_tgz).hexdigest(),
    'extractsize': str((sum(len(data) for data, _ in payload.values()) + 1023) // 1024),
}
outer = {'INFO': (''.join(f'{k}="{v}"\n' for k, v in info.items()).encode(), 0o644),
         'package.tgz': (package_tgz, 0o644), 'PACKAGE_ICON.PNG': (png(64), 0o644),
         'PACKAGE_ICON_256.PNG': (png(256), 0o644)}
for path in (HERE / 'conf').rglob('*'):
    if path.is_file():
        outer['conf/' + path.relative_to(HERE / 'conf').as_posix()] = (path.read_bytes().replace(b'\r\n', b'\n'), 0o644)
for path in (HERE / 'scripts').iterdir():
    outer['scripts/' + path.name] = (path.read_bytes().replace(b'\r\n', b'\n'), 0o755)
noop = b'#!/bin/sh\nexit 0\n'
for name in ['preuninst', 'postuninst']:
    outer['scripts/' + name] = (noop, 0o755)
outer['scripts/preupgrade'] = outer['scripts/preinst']
outer['scripts/postupgrade'] = outer['scripts/postinst']
spk = OUT / f'NASMediaFolders-armv8-{args.version}.spk'
spk.write_bytes(tar_bytes(outer))

def validate_directories(archive):
    entries = {item.name: item for item in archive.getmembers()}
    for item in entries.values():
        assert '\\' not in item.name, item.name
        if item.isdir():
            assert item.mode == 0o755, item.name
        for parent in PurePosixPath(item.name).parents:
            if str(parent) != '.':
                assert str(parent) in entries and entries[str(parent)].isdir(), item.name

# Validate the actual release archive, not just the source tree.
with tarfile.open(spk) as archive:
    validate_directories(archive)
    names = archive.getnames()
    assert len(names) == len(set(names))
    for item in archive.getmembers():
        assert (item.isfile() or item.isdir()) and not item.name.startswith('/') and '..' not in Path(item.name).parts
        if item.isfile() and item.name.startswith('scripts/'):
            assert b'\r' not in archive.extractfile(item).read()
    privilege = json.load(archive.extractfile('conf/privilege'))
    assert privilege == {'defaults': {'run-as': 'package'}}
    inner = archive.extractfile('package.tgz').read()
    assert hashlib.md5(inner).hexdigest() == info['checksum']
    with tarfile.open(fileobj=io.BytesIO(inner), mode='r:gz') as contents:
        validate_directories(contents)
        assert contents.extractfile('bin/nas-media-folders').read() == BINARY.read_bytes()
        assert contents.extractfile('web/index.html')
        for item in contents.getmembers():
            assert (item.isfile() or item.isdir()) and '..' not in Path(item.name).parts and not item.name.startswith('/')

print(json.dumps({'package': str(spk), 'sha256': hashlib.sha256(spk.read_bytes()).hexdigest(),
                  'bytes': spk.stat().st_size, 'payload_files': len(payload), 'archive_validation': 'PASS'}, indent=2))

