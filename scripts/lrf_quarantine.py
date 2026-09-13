#!/usr/bin/env python3
"""Plan, quarantine, or restore DJI LRF backups on macOS; never permanently delete.

Moves use exclusive same-volume rename. The saved plan supports recovery even if
interrupted halfway. File identity, size and mtime are checked before/after moves;
contents are never rewritten. Run with reel idle; a reel lock is held when supplied.
"""
import argparse
import contextlib
import ctypes
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import tempfile


def fingerprint(path):
    path = Path(path)
    if path.resolve() != path.absolute():
        raise ValueError(f"Symlink in path: {path}")
    s = path.lstat()
    if not stat.S_ISREG(s.st_mode):
        raise ValueError(f"Not a regular file: {path}")
    return dict(device=s.st_dev, inode=s.st_ino, size=s.st_size, mtime_ns=s.st_mtime_ns)


def check(path, expected):
    if fingerprint(path) != expected:
        raise ValueError(f"File changed since planning: {path}")


def atomic_write(path, data):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"Symlink: {path}")
    fd, tmp = tempfile.mkstemp(prefix='.reel-recovery-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)  # Only our temporary metadata file; never media.


def exclusive_move(src, dst):
    if sys.platform != 'darwin':
        raise RuntimeError('Exclusive rename implementation requires macOS')
    if Path(dst).parent.resolve() != Path(dst).parent.absolute():
        raise ValueError(f"Symlink in destination: {dst}")
    if os.stat(src).st_dev != os.stat(Path(dst).parent).st_dev:
        raise ValueError('Cross-volume move refused')
    lib = ctypes.CDLL(None, use_errno=True)
    rename = lib.renamex_np
    rename.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint]
    rename.restype = ctypes.c_int
    if rename(os.fsencode(src), os.fsencode(dst), 4):  # RENAME_EXCL
        raise OSError(ctypes.get_errno(), f'Exclusive rename failed: {src} -> {dst}')


def read_rows(path):
    text = Path(path).read_text()
    rows = [json.loads(line) for line in text.splitlines() if line.strip()]
    keys = [key(r) for r in rows]
    if len(keys) != len(set(keys)):
        raise ValueError(f'Duplicate state keys: {path}')
    return text, rows


def key(row):
    return (row['camera_profile'], row['base_name'], row['ext'])


def plan(root, quarantine, states, lock):
    root, quarantine = Path(root).absolute(), Path(quarantine).absolute()
    if root.resolve() != root or quarantine.resolve() != quarantine:
        raise ValueError('Root/quarantine must not contain symlinks')
    if root == quarantine or root in quarantine.parents or quarantine in root.parents:
        raise ValueError('Quarantine must be outside the backup folder')
    if quarantine.exists():
        raise ValueError('Use a new quarantine directory')
    ancestor = quarantine.parent
    while not ancestor.exists():
        ancestor = ancestor.parent
    device = root.stat().st_dev
    if ancestor.stat().st_dev != device:
        raise ValueError('Quarantine must be on the same volume')
    entries, untouched = [], []
    def walk_error(error):
        raise error
    for directory, dirs, files in os.walk(root, followlinks=False, onerror=walk_error):
        for name in dirs:
            if Path(directory, name).is_symlink():
                raise ValueError(f'Symlink directory: {directory}/{name}')
        for name in sorted(files):
            src = Path(directory, name)
            info = fingerprint(src)
            if info['device'] != device:
                raise ValueError(f'Nested volume: {src}')
            if src.suffix.lower() != '.lrf':
                untouched.append(dict(path=str(src), fingerprint=info))
                continue
            if not re.fullmatch(r'DJI_\d{14}_\d{4}_[A-Z]\.LRF', name, re.I):
                raise ValueError(f'Unrecognized LRF filename: {src}')
            siblings = [Path(directory, n) for n in files if n.lower() == src.stem.lower()+'.mp4']
            if len(siblings) != 1 or fingerprint(siblings[0])['size'] == 0:
                raise ValueError(f'Missing/ambiguous/empty MP4 companion: {src}')
            entries.append(dict(source=str(src), destination=str(quarantine/'files'/src.relative_to(root)), fingerprint=info))
    sources = {e['source'] for e in entries}
    state_plans = []
    for state_path in states:
        state_path = str(Path(state_path).absolute())
        fingerprint(state_path)
        original, rows = read_rows(state_path)
        edits = []
        for row in rows:
            changes = {}
            for path_field, verified_field in [('hd_path', 'hd_verified_at'), ('laptop_path', None)]:
                if row.get(path_field) in sources:
                    if row['ext'].upper() != 'LRF':
                        raise ValueError('Non-LRF state row points to an LRF')
                    changes[path_field] = ''
                    if verified_field:
                        changes[verified_field] = None
            if changes:
                edits.append(dict(key=list(key(row)), before={k: row[k] for k in changes}, after=changes))
        state_plans.append(dict(path=state_path, original=original, edits=edits))
    return dict(version=1, root=str(root), quarantine=str(quarantine), device=device,
                lock=lock, entries=entries, untouched=untouched, states=state_plans)


def state_updates(p, restore):
    updates = []
    for item in p['states']:
        original, rows = read_rows(item['path'])
        mapping = {key(r): r for r in rows}
        for edit in item['edits']:
            row = mapping[tuple(edit['key'])]
            before, after = (edit['after'], edit['before']) if restore else (edit['before'], edit['after'])
            current = {k: row.get(k) for k in before}
            if current == after:
                continue
            if current != before:
                raise ValueError(f"State changed; manual reconciliation required: {item['path']} {edit['key']}")
            row.update(after)
        # Preserve unrelated rows byte-for-byte, including unknown fields.
        lines = []
        for line in original.splitlines(keepends=True):
            if not line.strip():
                lines.append(line)
                continue
            row = json.loads(line)
            updated = mapping[key(row)]
            lines.append(line if updated == row else json.dumps(updated, separators=(',', ':'))+'\n')
        updates.append((item['path'], ''.join(lines)))
    return updates


@contextlib.contextmanager
def locked(path):
    if not path:
        yield
        return
    with open(path, 'a+') as f:
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
        # Leave the lockfile in place: unlinking can break concurrent locking.


def execute(p, restore=False):
    with locked(p.get('lock')):
        root, quarantine = Path(p['root']), Path(p['quarantine'])
        if root.resolve() != root or quarantine.resolve() != quarantine:
            raise ValueError('Symlink in root/quarantine')
        if root.stat().st_dev != p['device']:
            raise ValueError('Backup volume changed; re-plan before continuing')
        # Validate the entire batch and state before moving the first file.
        updates = state_updates(p, restore)
        for item in p['untouched']:
            check(item['path'], item['fingerprint'])
        pending = []
        for entry in p['entries']:
            src, dst = entry['source'], entry['destination']
            if not Path(src).is_relative_to(root) or not Path(dst).is_relative_to(quarantine/'files') or Path(src).suffix.lower() != '.lrf':
                raise ValueError('Invalid manifest path')
            if restore:
                src, dst = dst, src
            if os.path.lexists(src) and os.path.lexists(dst):
                raise FileExistsError(f'Both locations exist; refusing overwrite: {src}, {dst}')
            check(src if os.path.lexists(src) else dst, entry['fingerprint'])
            if os.path.lexists(src):
                pending.append((src, dst, entry['fingerprint']))
        if not restore:
            quarantine.mkdir(parents=True, exist_ok=True)
            manifest = quarantine/'manifest.json'
            if manifest.exists() and json.loads(manifest.read_text()) != p:
                raise ValueError('Quarantine belongs to another plan')
            atomic_write(manifest, json.dumps(p, indent=2)+'\n')
            if Path(__file__).resolve() != quarantine/'restore_lrf.py':
                shutil.copy2(__file__, quarantine/'restore_lrf.py')
            atomic_write(quarantine/'RESTORE.txt',
                         'Files are quarantined, not deleted. No automatic expiry.\n'
                         'To restore files and their reel state paths, with reel idle:\n'
                         'python3 "'+str(quarantine/'restore_lrf.py')+'" restore "'+str(manifest)+'"\n'
                         'This refuses overwrites and changes to the recorded files.\n'
                         'Unrelated file changes may require a manual review before restoration.\n')
        for src, dst, expected in pending:
            Path(dst).parent.mkdir(parents=True, exist_ok=True)
            check(src, expected)
            exclusive_move(src, dst)
            check(dst, expected)
        for item in p['untouched']:
            check(item['path'], item['fingerprint'])
        for path, data in updates:
            atomic_write(path, data)
        print(f"{'Restored' if restore else 'Quarantined'} {len(pending)} LRF files; "
              f"verified {len(p['untouched'])} other files unchanged; updated {len(updates)} state files.", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    create = sub.add_parser('plan')
    create.add_argument('--root', required=True)
    create.add_argument('--quarantine', required=True)
    create.add_argument('--manifest', required=True)
    create.add_argument('--state', action='append', default=[])
    create.add_argument('--lock')
    for action in ('apply', 'restore'):
        sub.add_parser(action).add_argument('manifest')
    args = parser.parse_args()
    if args.action == 'plan':
        p = plan(args.root, args.quarantine, args.state, args.lock)
        with open(args.manifest, 'x') as f:
            json.dump(p, f, indent=2)
            f.write('\n')
        print(json.dumps(dict(files=len(p['entries']), bytes=sum(e['fingerprint']['size'] for e in p['entries']),
                              protected_files=len(p['untouched']), state_rows=[len(s['edits']) for s in p['states']],
                              manifest=args.manifest), indent=2))
    else:
        execute(json.loads(Path(args.manifest).read_text()), restore=args.action == 'restore')


if __name__ == '__main__':
    main()
