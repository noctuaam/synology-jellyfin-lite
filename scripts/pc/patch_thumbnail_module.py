"""Apply the exact tested 42-byte compatibility change to a user-supplied module.
No Synology executable is distributed with this project.
"""
import argparse
import hashlib
import json
from pathlib import Path


def patch(original, recipe):
    if hashlib.sha256(original).hexdigest() != recipe['original_sha256']:
        raise ValueError('Unsupported original module checksum; refusing to patch')
    output = bytearray(original)
    for change in recipe['changes']:
        start = change['offset']
        before, after = bytes.fromhex(change['before']), bytes.fromhex(change['after'])
        if len(before) != len(after) or output[start:start+len(before)] != before:
            raise ValueError('Patch precondition failed')
        output[start:start+len(before)] = after
    if hashlib.sha256(output).hexdigest() != recipe['patched_sha256']:
        raise ValueError('Patched module checksum mismatch')
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('original', type=Path)
    parser.add_argument('output', type=Path)
    args = parser.parse_args()
    recipe = json.loads(Path(__file__).with_name('mediaserver-thumbnail-patch.json').read_text())
    result = patch(args.original.read_bytes(), recipe)
    with args.output.open('xb') as output:
        output.write(result)
    print('PASS:', recipe['patched_sha256'])


if __name__ == '__main__':
    main()
