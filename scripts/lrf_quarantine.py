#!/usr/bin/env python3
"""Plan, quarantine, or restore DJI LRF backups on macOS; never permanently delete.

Moves use exclusive same-volume rename. The saved plan supports recovery even if
interrupted halfway. File identity, size and mtime are checked before/after moves;
contents are never rewritten. Apply and restore lock the validated managed archive.
"""
import argparse
import contextlib
import ctypes
import fcntl
import json
import os
from pathlib import Path
import re
import hashlib
import stat
import sys


@contextlib.contextmanager
def directory(path, create=False):
    path = Path(path).absolute()
    if '..' in path.parts:
        raise ValueError('Path traversal')
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in path.parts[1:]:
            if create:
                try:
                    os.mkdir(part, 0o700, dir_fd=fd)
                except FileExistsError:
                    pass
            new = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            if create:
                try:
                    # Retry may encounter a directory left by an earlier failed sync.
                    os.fsync(fd)
                except BaseException:
                    os.close(new)
                    raise
            os.close(fd)
            fd = new
        yield fd
    finally:
        os.close(fd)


def regular(fd):
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError('Expected regular unaliased file')
    return info


def fingerprint(path, parent_fd=None):
    path = Path(path)
    with directory(path.parent) if parent_fd is None else contextlib.nullcontext(parent_fd) as parent:
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        try:
            s = regular(fd)
            h = hashlib.sha256()
            while True:
                chunk = os.read(fd, 1024*1024)
                if not chunk:
                    break
                h.update(chunk)
            after = regular(fd)
            if (s.st_size, s.st_mtime_ns, s.st_ctime_ns) != (after.st_size, after.st_mtime_ns, after.st_ctime_ns):
                raise ValueError('File changed while hashing')
            return dict(device=s.st_dev, inode=s.st_ino, size=s.st_size, mtime_ns=s.st_mtime_ns, sha256=h.hexdigest())
        finally:
            os.close(fd)


def check(path, expected, parent_fd=None):
    current = fingerprint(path, parent_fd)
    # Legacy manifests lack hashes; accept metadata only for standalone recovery.
    if any(current.get(k) != v for k, v in expected.items()):
        raise ValueError(f"File changed since planning: {path}")


def artifact(path, data, create=True, parent_fd=None):
    """Create exclusively, or reuse an identical regular unaliased artifact."""
    path = Path(path)
    data = data.encode() if isinstance(data, str) else data
    with directory(path.parent) if parent_fd is None else contextlib.nullcontext(parent_fd) as parent:
        try:
            fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        except FileNotFoundError:
            if not create:
                return
            fd = os.open(path.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
            with os.fdopen(fd, 'wb') as f:
                f.write(data)
                f.flush()
                os.fsync(f.fileno())
            os.fsync(parent)
        else:
            with os.fdopen(fd, 'rb') as f:
                regular(f.fileno())
                if f.read() != data:
                    raise ValueError(f'Conflicting recovery artifact: {path}')


def atomic_write(path, data):
    path = Path(path)
    with directory(path.parent) as parent:
        # Metadata replacement never follows aliases or applies to media.
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        try:
            regular(fd)
        finally:
            os.close(fd)
        name = '.reel-state-' + os.urandom(16).hex()
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        with os.fdopen(fd, 'w') as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.rename(name, path.name, src_dir_fd=parent, dst_dir_fd=parent)
        os.fsync(parent)


def directory_location(fd, fallback):
    if sys.platform == 'darwin':
        try:
            # Darwin sys/fcntl.h: F_GETPATH = 50, MAXPATHLEN = 1024.
            return os.fsdecode(fcntl.fcntl(fd, 50, b'\0'*1024).split(b'\0', 1)[0])
        except OSError:
            pass
    return str(fallback)


def check_directory(path, fd):
    try:
        with directory(path) as current:
            a, b = os.fstat(fd), os.fstat(current)
            if (a.st_dev, a.st_ino) == (b.st_dev, b.st_ino):
                return
    except OSError:
        pass
    raise ValueError(f'Directory changed: {path}; retained directory: {directory_location(fd, path)}')


def exclusive_move(src, dst, source_fd=None, destination_fd=None):
    if sys.platform != 'darwin':
        raise RuntimeError('Exclusive rename implementation requires macOS')
    src, dst = Path(src), Path(dst)
    with contextlib.ExitStack() as stack:
        a = source_fd if source_fd is not None else stack.enter_context(directory(src.parent))
        b = destination_fd if destination_fd is not None else stack.enter_context(directory(dst.parent))
        check_directory(src.parent, a)
        check_directory(dst.parent, b)
        if os.fstat(a).st_dev != os.fstat(b).st_dev:
            raise ValueError('Cross-volume move refused')
        lib = ctypes.CDLL(None, use_errno=True)
        rename = lib.renameatx_np
        rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
        rename.restype = ctypes.c_int
        if rename(a, os.fsencode(src.name), b, os.fsencode(dst.name), 4):
            raise OSError(ctypes.get_errno(), f'Exclusive rename failed: {src} -> {dst}')
        os.fsync(b)
        os.fsync(a)
        check_directory(src.parent, a)
        check_directory(dst.parent, b)


PROTOCOL = '{"version":2,"media_publication":"exclusive","recovery":"retain"}\n'


def resolve_archive(root, explicit=None):
    """Find the unique managed ancestor; an unmarked archive needs an explicit root."""
    root = Path(root).absolute()
    with directory(root) as fd:
        device = os.fstat(fd).st_dev
    marked = []
    for candidate in (root, *root.parents):
        with directory(candidate) as fd:
            if os.fstat(fd).st_dev != device:
                break
            try:
                marker = os.open('.reel-protocol.json', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
            except FileNotFoundError:
                continue
            with os.fdopen(marker, 'rb') as f:
                regular(f.fileno())
                if f.read(len(PROTOCOL)+1) != PROTOCOL.encode():
                    raise ValueError(f'Incompatible archive marker: {candidate}')
            marked.append(candidate)
    if len(marked) > 1:
        raise ValueError('Ambiguous nested archive markers; reconcile archive roots before proceeding')
    selected = Path(explicit).absolute() if explicit is not None else (marked[0] if marked else None)
    if selected is None:
        raise ValueError('No managed archive marker found; specify --archive-root explicitly')
    if selected != root and selected not in root.parents:
        raise ValueError('Selection must be inside the managed archive root')
    if marked and selected != marked[0]:
        raise ValueError('Explicit archive root conflicts with the managed archive marker')
    with directory(selected) as fd:
        if os.fstat(fd).st_dev != device:
            raise ValueError('Selection and archive are on different devices')
    return selected


def read_rows(path):
    path = Path(path)
    with directory(path.parent) as parent:
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        with os.fdopen(fd) as f:
            regular(f.fileno())
            text = f.read()
    rows = [json.loads(line) for line in text.splitlines() if line.strip()]
    if any(r.get("schema_version", 1) not in (1, 2) for r in rows):
        raise ValueError("Unsupported state schema")
    keys = [key(r) for r in rows]
    if len(keys) != len(set(keys)):
        raise ValueError(f'Duplicate state keys: {path}')
    return text, rows


def key(row):
    return (row['camera_profile'], row['base_name'], row['ext'])


def plan(root, quarantine, states, lock, archive_root=None):
    root, quarantine = Path(root).absolute(), Path(quarantine).absolute()
    if root.resolve() != root or quarantine.resolve() != quarantine:
        raise ValueError('Root/quarantine must not contain symlinks')
    archive_root = resolve_archive(root, archive_root)
    if archive_root == quarantine or archive_root in quarantine.parents or quarantine in archive_root.parents:
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
            if name in ('reel.lock', '.reel-archive.lock', '.reel-protocol.json'):
                continue
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
    return dict(version=1, root=str(root), archive_root=str(archive_root), quarantine=str(quarantine), device=device,
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
    path = Path(path)
    with directory(path.parent) as parent:
        fd = os.open(path.name, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=parent)
        try:
            regular(fd)
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            yield
        finally:
            os.close(fd)  # Permanent lockfile: never unlink.


@contextlib.contextmanager
def transaction_locks(p, archive_root=None):
    selected = resolve_archive(p['root'], archive_root or p.get('archive_root'))
    # Same ordering as Go: local state directories, then shared archive root.
    local = {str(Path(s['path']).parent/'reel.lock') for s in p['states'] if Path(s['path']).name != '.reel-state.jsonl'}
    mirrors = {str(Path(s['path']).parent/'reel.lock') for s in p['states'] if Path(s['path']).name == '.reel-state.jsonl'}
    if p.get('lock'):
        local.add(str(Path(p['lock']).absolute()))
    archive = str(selected/'.reel-archive.lock')
    media = str(selected/'reel.lock')
    with contextlib.ExitStack() as stack:
        acquired = set()
        for path in sorted(local) + [archive, media] + sorted(mirrors):
            if path not in acquired:
                stack.enter_context(locked(path))
                acquired.add(path)
        if resolve_archive(p['root'], selected) != selected:
            raise ValueError('Archive root changed while acquiring locks')
        artifact(selected/'.reel-protocol.json', PROTOCOL)
        yield selected


def execute(p, restore=False, archive_root=None):
    with transaction_locks(p, archive_root) as archive, contextlib.ExitStack() as handles:
        if p.get('version') != 1:
            raise ValueError('Unsupported manifest version')
        root, quarantine = Path(p['root']), Path(p['quarantine'])
        pinned = {}

        def pin(path, create=False):
            path = Path(path)
            if path not in pinned:
                pinned[path] = handles.enter_context(directory(path, create=create))
            return pinned[path]

        def check_pins():
            for path, fd in pinned.items():
                check_directory(path, fd)

        pin(archive)
        pin(root)
        if root.resolve() != root or quarantine.resolve() != quarantine:
            raise ValueError('Symlink in root/quarantine')
        if os.fstat(pin(root)).st_dev != p['device']:
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
        # Retain all endpoint parents and the recovery root through the entire
        # transaction. Artifacts and media use these same validated descriptors.
        recovery_fd = pin(quarantine, create=True) if not restore or pending else None
        endpoints = []
        for src, dst, expected in pending:
            a = pin(Path(src).parent)
            b = pin(Path(dst).parent, create=True)
            if os.fstat(a).st_dev != p['device'] or os.fstat(b).st_dev != p['device']:
                raise ValueError('Endpoint changed device')
            endpoints.append((src, dst, expected, a, b))
        if not restore:
            manifest = quarantine/'manifest.json'
            artifacts = {
                manifest: json.dumps(p, indent=2)+'\n',
                quarantine/'restore_lrf.py': Path(__file__).read_bytes(),
                quarantine/'RESTORE.txt':
                    'Files are quarantined, not deleted. No automatic expiry.\n'
                    'To restore files and their reel state paths:\n'
                    'python3 "'+str(quarantine/'restore_lrf.py')+'" restore "'+str(manifest)+'"\n'
                    'This refuses overwrites and changes to the recorded files.\n',
            }
            for path, data in artifacts.items():
                check_pins()
                artifact(path, data, create=False, parent_fd=recovery_fd)
            for path, data in artifacts.items():
                check_pins()
                artifact(path, data, parent_fd=recovery_fd)
        try:
            for src, dst, expected, a, b in endpoints:
                check_pins()
                check(src, expected, parent_fd=a)
                exclusive_move(src, dst, source_fd=a, destination_fd=b)
                check(dst, expected, parent_fd=b)
                check_pins()
            check_pins()
        except (OSError, ValueError) as error:
            location = directory_location(recovery_fd, quarantine) if recovery_fd is not None else str(quarantine)
            raise ValueError(f'{error}; recovery material retained at {location}') from error
        for item in p['untouched']:
            check(item['path'], item['fingerprint'])
        for path, data in updates:
            check_pins()
            atomic_write(path, data)
        check_pins()
        print(f"{'Restored' if restore else 'Quarantined'} {len(pending)} LRF files; "
              f"verified {len(p['untouched'])} other files unchanged; updated {len(updates)} state files.", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    create = sub.add_parser('plan')
    create.add_argument('--root', required=True)
    create.add_argument('--quarantine', required=True)
    create.add_argument('--archive-root', help='managed archive root (required when no marker exists)')
    create.add_argument('--manifest', required=True)
    create.add_argument('--state', action='append', default=[])
    create.add_argument('--lock')
    for action in ('apply', 'restore'):
        operation = sub.add_parser(action)
        operation.add_argument('manifest')
        operation.add_argument('--archive-root', help='managed root for a legacy manifest without archive identity')
    args = parser.parse_args()
    if args.action == 'plan':
        p = plan(args.root, args.quarantine, args.state, args.lock, args.archive_root)
        with open(args.manifest, 'x') as f:
            json.dump(p, f, indent=2)
            f.write('\n')
        print(json.dumps(dict(files=len(p['entries']), bytes=sum(e['fingerprint']['size'] for e in p['entries']),
                              protected_files=len(p['untouched']), state_rows=[len(s['edits']) for s in p['states']],
                              manifest=args.manifest), indent=2))
    else:
        execute(json.loads(Path(args.manifest).read_text()), restore=args.action == 'restore', archive_root=args.archive_root)


if __name__ == '__main__':
    main()
