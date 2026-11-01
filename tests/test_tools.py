import hashlib
import importlib.util
import json
import os
import io
import struct
import subprocess
import sys
import tarfile
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


covers = load('covers', 'scripts/pc/generate_covers.py')
patcher = load('patcher', 'scripts/pc/patch_thumbnail_module.py')


class ToolsTest(unittest.TestCase):
    def test_publish_never_overwrites_existing_cover(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, destination = root/'generated.jpg', root/'existing.jpg'
            source.write_bytes(b'new cover')
            destination.write_bytes(b'keep original cover')
            self.assertFalse(covers.publish(source, destination))
            self.assertEqual(destination.read_bytes(), b'keep original cover')
            self.assertTrue(covers.publish(source, root/'new.jpg'))
            self.assertEqual(source.read_bytes(), b'new cover')
            self.assertEqual(list(root.glob('.nas-preview-*')), [])

    def test_inventory_excludes_cache_and_temporary_directories(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative in ['movie.mkv', 'series/01.mp4', '@eaDir/cache.mp4', '.partial/work.mp4', '#recycle/deleted.mkv']:
                path = root/relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'media')
            self.assertEqual({p.relative_to(root).as_posix() for p in covers.inventory(root)}, {'movie.mkv', 'series/01.mp4'})

    def test_metadata_merge_preserves_other_files_and_releases_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory)/'metadata.jsonl'
            covers.merge_metadata(destination, {'RelPath': 'Series/01.mkv', 'Size': 10})
            covers.merge_metadata(destination, {'RelPath': 'Movie.mkv', 'Size': 20})
            covers.merge_metadata(destination, {'RelPath': 'series/01.mkv', 'Size': 30})
            rows = [json.loads(line) for line in destination.read_text().splitlines()]
            self.assertEqual(len(rows), 2)
            self.assertEqual(sorted(row['Size'] for row in rows), [20, 30])
            self.assertFalse(destination.with_name(destination.name+'.lock').exists())
            before = destination.read_bytes()
            lock = destination.with_name(destination.name+'.lock')
            lock.write_bytes(b'another job')
            with self.assertRaises(FileExistsError):
                covers.merge_metadata(destination, {'RelPath': 'Other', 'Size': 50})
            self.assertEqual(destination.read_bytes(), before)

    def test_package_directories_use_posix_paths_and_readable_modes(self):
        # Exercise the real packager on Windows as well as Unix: native Path
        # separators must never leak into a Linux package archive.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root/'server'
            data = bytearray(64)
            data[:4] = b'\x7fELF'
            struct.pack_into('<H', data, 18, 183)
            binary.write_bytes(data)
            web = root/'web'
            (web/'themes/dark').mkdir(parents=True)
            (web/'index.html').write_text('test')
            (web/'themes/dark/theme.css').write_text('html{color:white}')
            subprocess.run([sys.executable, str(ROOT/'packaging/synology/build.py'),
                            '--binary', str(binary), '--web', str(web),
                            '--output', str(root/'output')], check=True,
                           capture_output=True)
            package = next((root/'output').glob('*.spk'))
            with tarfile.open(package) as outer:
                with tarfile.open(fileobj=io.BytesIO(outer.extractfile('package.tgz').read()), mode='r:gz') as inner:
                    for name in ['web', 'web/themes', 'web/themes/dark']:
                        entry = inner.getmember(name)
                        self.assertTrue(entry.isdir())
                        self.assertEqual(entry.mode, 0o755)
                    self.assertTrue(all('\\' not in entry.name for entry in inner))

    def test_unknown_native_module_is_refused(self):
        recipe = json.loads((ROOT/'scripts/pc/mediaserver-thumbnail-patch.json').read_text())
        with self.assertRaises(ValueError):
            patcher.patch(b'unsupported module', recipe)


if __name__ == '__main__':
    unittest.main()
