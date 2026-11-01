"""Generate JPEG sidecars on this PC; media originals are opened read-only."""
from pathlib import Path
import argparse, datetime, hashlib, json, os, shutil, subprocess, sys, time, uuid
HERE = Path(__file__).resolve().parent
if os.environ.get('NMF_PYTHON_PACKAGES'):
    sys.path.insert(0, os.environ['NMF_PYTHON_PACKAGES'])
from PIL import Image, ImageStat
EXTS = {'.mkv', '.mp4', '.m4v', '.avi', '.mov', '.ts', '.m2ts', '.webm', '.mpg', '.mpeg', '.wmv', '.flv', '.vob', '.3gp'}
SKIP = {'#recycle', '@eadir', '@tmp', '@appdata', 'system volume information', '$recycle.bin'}

def now():
    return datetime.datetime.now().astimezone().isoformat(timespec='seconds')

def inventory(root):
    files = []

    def walk_error(err):
        raise err
    for folder, dirs, names in os.walk(root, followlinks=False, onerror=walk_error):
        dirs[:] = sorted((d for d in dirs if d.lower() not in SKIP and (not d.startswith(('@', '.'))) and (not Path(folder, d).is_symlink())))
        for name in sorted(names):
            p = Path(folder, name)
            if p.suffix.lower() in EXTS and (not p.is_symlink()):
                files.append(p)
    return files

def run(command, timeout):
    options = {'creationflags': subprocess.BELOW_NORMAL_PRIORITY_CLASS | subprocess.CREATE_NO_WINDOW} if os.name == 'nt' else {}
    return subprocess.run([str(a) for a in command], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, **options)

def check_jpeg(p):
    with Image.open(p) as im:
        if im.format != 'JPEG':
            raise ValueError('output is not JPEG')
        im.load()
        width, height = im.size
        thumb = im.convert('L')
        thumb.thumbnail((160, 90))
        stat = ImageStat.Stat(thumb)
        return {'width': width, 'height': height, 'luma': round(stat.mean[0], 2), 'contrast': round(stat.stddev[0], 2)}

def render(ffmpeg, ffprobe, p, cache):
    meta = run([ffprobe, '-v', 'error', '-probesize', '5000000', '-analyzeduration', '3000000', '-show_format', '-show_streams', '-of', 'json', p], 90)
    if meta.returncode:
        raise RuntimeError(meta.stderr.decode('utf-8', 'replace')[-1500:])
    info = json.loads(meta.stdout)
    streams = [v for v in info.get('streams', []) if v.get('codec_type') == 'video' and (not v.get('disposition', {}).get('attached_pic'))]
    if not streams:
        raise RuntimeError('no video stream')
    stream = streams[0]
    duration = float(info.get('format', {}).get('duration') or stream.get('duration') or 0)
    transfer = stream.get('color_transfer', '')
    hdr = transfer in ('smpte2084', 'arib-std-b67')
    scale = "scale=w='min(1280,iw)':h=-2:flags=lanczos"
    tonemap = 'zscale=t=linear:npl=100,format=gbrpf32le,tonemap=hable:desat=0,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p,'
    offsets = [min(600, duration * 0.12), min(900, duration * 0.3), min(1200, duration * 0.5)] if duration > 1 else [0, 5, 15]
    best = None
    for attempt, offset in enumerate(dict.fromkeys(offsets)):
        local = cache / f'candidate-{attempt}.jpg'
        args = [ffmpeg, '-nostdin', '-hide_banner', '-loglevel', 'error', '-y', '-threads', '2', '-ss', f'{offset:.3f}', '-i', p, '-map', f"0:{stream['index']}", '-frames:v', '1', '-an', '-sn', '-dn', '-vf', (tonemap if hdr else '') + scale, '-c:v', 'mjpeg', '-threads', '2', '-q:v', '3', '-update', '1', local]
        result = run(args, 150)
        if result.returncode:
            raise RuntimeError(result.stderr.decode('utf-8', 'replace')[-1500:])
        if not local.exists() or local.stat().st_size < 500:
            continue
        stats = check_jpeg(local)
        score = stats['contrast'] + (0 if 12 < stats['luma'] < 243 else -100)
        if best is None or score > best[0]:
            best = (score, local, stats, offset)
        if 12 < stats['luma'] < 243 and stats['contrast'] > 12:
            break
    if best is None:
        raise RuntimeError('no usable JPEG extracted')
    _, local, stats, offset = best
    if stats['luma'] < 3 or stats['luma'] > 252:
        raise RuntimeError('all sampled frames are nearly blank; not publishing')
    return (local, dict(stats, offset=round(offset, 3), duration=duration, hdr_tonemapped=hdr, streams=info.get('streams', []), format=info.get('format', {})))

def publish(local, target):
    if target.exists():
        return False
    temp = target.parent / ('.nas-preview-' + uuid.uuid4().hex + '.tmp')
    try:
        with local.open('rb') as source, temp.open('xb') as dest:
            shutil.copyfileobj(source, dest)
        if os.name == 'nt':
            os.rename(temp, target)
        else:
            os.link(temp, target)
        return True
    except FileExistsError:
        return False
    finally:
        if temp.exists():
            temp.unlink()

def metadata_entry(video, root, metadata):
    streams = []
    for stream in metadata['streams']:
        kind = {'video': 'Video', 'audio': 'Audio', 'subtitle': 'Subtitle'}.get(stream.get('codec_type'))
        if not kind:
            continue
        tags = stream.get('tags', {})
        disposition = stream.get('disposition', {})
        item = dict(Index=stream['index'], Type=kind, Codec=stream.get('codec_name', ''),
                    Language=tags.get('language', 'und'), Title=tags.get('title', ''),
                    IsDefault=bool(disposition.get('default')), IsForced=bool(disposition.get('forced')),
                    IsHearingImpaired=bool(disposition.get('hearing_impaired')))
        for source, target in [('width', 'Width'), ('height', 'Height'), ('channels', 'Channels'),
                               ('sample_rate', 'SampleRate'), ('bit_rate', 'BitRate')]:
            if stream.get(source):
                item[target] = int(stream[source])
        streams.append(item)
    return dict(RelPath=video.relative_to(root).as_posix(), Size=video.stat().st_size,
                Ticks=round(metadata['duration'] * 10000000), Streams=streams)


def merge_metadata(destination, entry):
    # One PC job at a time: a lock prevents competing writers losing entries.
    lock = destination.with_name(destination.name + '.lock')
    descriptor = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    temp = destination.with_name('.' + destination.name + '.' + uuid.uuid4().hex + '.tmp')
    try:
        records = {}
        if destination.exists():
            for line in destination.read_text(encoding='utf-8-sig').splitlines():
                row = json.loads(line)
                records[row['RelPath'].replace('\\', '/').casefold()] = row
        records[entry['RelPath'].casefold()] = entry
        with temp.open('x', encoding='utf-8', newline='\n') as output:
            for key in sorted(records):
                output.write(json.dumps(records[key], ensure_ascii=False) + '\n')
        os.replace(temp, destination)
    finally:
        os.close(descriptor)
        lock.unlink()
        temp.unlink(missing_ok=True)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--root', required=True)
    ap.add_argument('--ffmpeg', required=True)
    ap.add_argument('--ffprobe', required=True)
    ap.add_argument('--limit', type=int, default=0)
    ap.add_argument('--plan', action='store_true')
    ap.add_argument('--state-dir', type=Path, default=HERE / '.state')
    ap.add_argument('--min-age', type=int, default=300)
    ap.add_argument('--metadata-file', type=Path, help='Optional shared JSONL metadata file; merged atomically')
    args = ap.parse_args()
    root = Path(args.root)
    state = args.state_dir.resolve()
    state.mkdir(parents=True, exist_ok=True)
    if args.limit < 0 or args.min_age < 0:
        raise SystemExit('Limit and min-age must be nonnegative')
    if not root.is_dir():
        raise SystemExit('Video share is not accessible; authenticate in Windows first')
    jobs = inventory(root)
    (state / 'inventory.json').write_text(json.dumps([str(p.relative_to(root)) for p in jobs], ensure_ascii=False, indent=2), encoding='utf-8')
    existing = sum((p.with_suffix('.jpg').exists() for p in jobs))
    print(json.dumps(dict(time=now(), videos=len(jobs), existing=existing, pending=len(jobs) - existing, root=str(root)), ensure_ascii=False), flush=True)
    if args.plan:
        return
    probe = root / ('.nas-preview-access-' + uuid.uuid4().hex + '.tmp')
    try:
        with probe.open('xb') as f:
            f.write(b'preview access check')
    finally:
        if probe.exists():
            probe.unlink()
    created = skipped = errors = 0
    started = time.monotonic()
    processed = 0
    log = state / 'preview-results.jsonl'
    for number, p in enumerate(jobs, 1):
        target = p.with_suffix('.jpg')
        if target.exists():
            skipped += 1
            continue
        if args.limit and processed >= args.limit:
            break
        if time.time() - p.stat().st_mtime < args.min_age:
            skipped += 1
            continue
        processed += 1
        rel = str(p.relative_to(root))
        cache = state / 'frames' / hashlib.sha256(rel.encode()).hexdigest()[:20]
        cache.mkdir(parents=True, exist_ok=True)
        record = {'time': now(), 'video': rel, 'cover': str(target.relative_to(root))}
        try:
            before = p.stat()
            local, metadata = render(args.ffmpeg, args.ffprobe, p, cache)
            after = p.stat()
            if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
                raise RuntimeError('Media file changed while rendering; retry after download completes')
            record.update(metadata)
            if args.metadata_file:
                merge_metadata(args.metadata_file, metadata_entry(p, root, metadata))
            if publish(local, target):
                created += 1
                record['status'] = 'created'
                record['bytes'] = target.stat().st_size
            else:
                skipped += 1
                record['status'] = 'existing'
        except Exception as ex:
            errors += 1
            record.update(status='error', error=str(ex))
        finally:
            # Remove only candidate files created by this job, never source media.
            for candidate in cache.glob('candidate-*.jpg'):
                candidate.unlink(missing_ok=True)
            if not any(cache.iterdir()):
                cache.rmdir()
        with log.open('a', encoding='utf-8') as f:
            f.write(json.dumps(record, ensure_ascii=False) + '\n')
        status = {'number': number, 'total': len(jobs), 'created': created, 'skipped': skipped, 'errors': errors, 'file': rel, 'status': record['status'], 'elapsed_seconds': round(time.monotonic() - started)}
        (state / 'status.json').write_text(json.dumps(status, ensure_ascii=False, indent=2), encoding='utf-8')
        print(json.dumps(status, ensure_ascii=False), flush=True)
    summary = dict(time=now(), videos=len(jobs), created=created, skipped=skipped, errors=errors, elapsed_seconds=round(time.monotonic() - started), limited=bool(args.limit))
    (state / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding='utf-8')
    print('SUMMARY ' + json.dumps(summary, ensure_ascii=False), flush=True)
    return 1 if errors else 0
if __name__ == '__main__':
    raise SystemExit(main())
