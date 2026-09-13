import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import os

import lrf_quarantine as q


class QuarantineTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name).resolve()
        self.root = self.base/'backup'
        self.root.mkdir()
        (self.root/'.reel-protocol.json').write_text(q.PROTOCOL)
        self.recovery = self.base/'recovery'
        self.lrf = self.root/'DJI_20260912123456_0001_D.LRF'
        self.mp4 = self.lrf.with_suffix('.MP4')
        self.lrf.write_bytes(b'proxy contents')
        self.mp4.write_bytes(b'original video contents')
        self.state = self.base/'state.jsonl'
        self.row = dict(camera_profile='DJI', base_name=self.lrf.stem, ext='LRF',
                        hd_path=str(self.lrf), hd_verified_at='2026-09-12', laptop_path='',
                        unknown_field='must survive')
        self.other = dict(camera_profile='DJI', base_name=self.mp4.stem, ext='MP4',
                          hd_path=str(self.mp4), hd_verified_at='2026-09-12')
        self.state.write_text(json.dumps(self.row)+'\n'+json.dumps(self.other)+'\n')

    def plan(self):
        return q.plan(self.root, self.recovery, [self.state], None)

    def test_round_trip_keeps_contents_identity_and_state(self):
        p = self.plan()
        identity = q.fingerprint(self.lrf)
        original_state = self.state.read_text()
        q.execute(p)
        self.assertFalse(self.lrf.exists())
        dest = Path(p['entries'][0]['destination'])
        self.assertEqual(dest.read_bytes(), b'proxy contents')
        self.assertEqual(q.fingerprint(dest), identity)
        self.assertEqual(self.mp4.read_bytes(), b'original video contents')
        self.assertEqual(json.loads(self.state.read_text().splitlines()[0])['hd_path'], '')
        self.assertEqual(json.loads((self.recovery/'manifest.json').read_text())['states'][0]['original'], original_state)
        q.execute(p)  # Resume a completed run safely.
        q.execute(p, restore=True)
        self.assertEqual(q.fingerprint(self.lrf), identity)
        self.assertEqual(json.loads(self.state.read_text().splitlines()[0]), self.row)
        self.assertEqual(self.state.read_text().splitlines()[1], original_state.splitlines()[1])
        q.execute(p, restore=True)

    def test_restore_never_overwrites(self):
        p = self.plan()
        q.execute(p)
        self.lrf.write_bytes(b'new file')
        with self.assertRaises(FileExistsError):
            q.execute(p, restore=True)
        self.assertEqual(self.lrf.read_bytes(), b'new file')
        self.assertTrue(Path(p['entries'][0]['destination']).exists())

    def test_exclusive_move_never_overwrites(self):
        dest = self.base/'occupied.LRF'
        dest.write_bytes(b'keep me')
        with self.assertRaises(OSError):
            q.exclusive_move(self.lrf, dest)
        self.assertEqual(dest.read_bytes(), b'keep me')
        self.assertTrue(self.lrf.exists())

    def test_changed_mp4_aborts_before_moving_anything(self):
        p = self.plan()
        self.mp4.write_bytes(b'changed')
        with self.assertRaises(ValueError):
            q.execute(p)
        self.assertTrue(self.lrf.exists())
        self.assertFalse(self.recovery.exists())

    def test_changed_lrf_aborts(self):
        p = self.plan()
        self.lrf.write_bytes(b'changed')
        with self.assertRaises(ValueError):
            q.execute(p)
        self.assertFalse(self.recovery.exists())

    def test_missing_companion_aborts_plan(self):
        self.mp4.rename(self.mp4.with_suffix('.other'))
        with self.assertRaises((ValueError, OSError)):
            self.plan()

    def test_symlink_aborts_plan(self):
        (self.root/'link.LRF').symlink_to(self.lrf)
        with self.assertRaises((ValueError, OSError)):
            self.plan()

    def test_restore_after_interruption_before_state_update(self):
        p = self.plan()
        dest = Path(p['entries'][0]['destination'])
        dest.parent.mkdir(parents=True)
        q.exclusive_move(self.lrf, dest)
        q.execute(p, restore=True)
        self.assertTrue(self.lrf.exists())
        self.assertEqual(json.loads(self.state.read_text().splitlines()[0]), self.row)

    def test_concurrent_state_edit_aborts(self):
        p = self.plan()
        self.row['hd_path'] = '/another/backup.LRF'
        self.state.write_text(json.dumps(self.row)+'\n'+json.dumps(self.other)+'\n')
        with self.assertRaises(ValueError):
            q.execute(p)
        self.assertTrue(self.lrf.exists())

    def test_helper_aliases_preserve_mp4_and_lrf(self):
        import os
        for kind in ('symlink', 'hardlink'):
            with self.subTest(kind=kind):
                p = self.plan()
                self.recovery.mkdir()
                helper = self.recovery/'restore_lrf.py'
                if kind == 'symlink':
                    helper.symlink_to(self.mp4)
                else:
                    os.link(self.mp4, helper)
                with self.assertRaises((ValueError, OSError)):
                    q.execute(p)
                self.assertEqual(self.mp4.read_bytes(), b'original video contents')
                self.assertTrue(self.lrf.exists())
                import shutil
                shutil.rmtree(self.recovery)

    def test_conflicting_artifact_stops_before_move(self):
        p = self.plan()
        self.recovery.mkdir()
        helper = self.recovery/'RESTORE.txt'
        helper.write_text('keep this')
        with self.assertRaises(ValueError):
            q.execute(p)
        self.assertEqual(helper.read_text(), 'keep this')
        self.assertTrue(self.lrf.exists())

    def test_same_metadata_content_change_is_rejected(self):
        import os
        p = self.plan()
        info = self.mp4.stat()
        self.mp4.write_bytes(b'x'*info.st_size)
        os.utime(self.mp4, ns=(info.st_atime_ns, info.st_mtime_ns))
        with self.assertRaises(ValueError):
            q.execute(p)
        self.assertTrue(self.lrf.exists())

    def test_subtree_uses_managed_archive_lock(self):
        selected = self.root/'camera-session'
        selected.mkdir()
        lrf = selected/self.lrf.name
        mp4 = selected/self.mp4.name
        self.lrf.rename(lrf)
        self.mp4.rename(mp4)
        p = q.plan(selected, self.recovery, [], None)
        self.assertEqual(p['archive_root'], str(self.root))
        with q.locked(self.root/'.reel-archive.lock'):
            with self.assertRaises(BlockingIOError):
                q.execute(p)
        self.assertTrue(lrf.exists())
        self.assertFalse((selected/'.reel-archive.lock').exists())
        # An explicit subtree cannot override the managed ancestor.
        with self.assertRaises(ValueError):
            q.plan(selected, self.recovery, [], None, selected)
        q.execute(p)
        q.execute(p, restore=True)
        self.assertEqual(lrf.read_bytes(), b'proxy contents')

    def test_archive_root_must_be_unambiguous(self):
        (self.root/'.reel-protocol.json').unlink()
        with self.assertRaisesRegex(ValueError, '--archive-root'):
            self.plan()
        p = q.plan(self.root, self.recovery, [], None, self.root)
        self.assertEqual(p['archive_root'], str(self.root))
        q.execute(p)
        # A second marker makes ancestor selection ambiguous, even explicitly.
        (self.base/'.reel-protocol.json').write_text(q.PROTOCOL)
        with self.assertRaisesRegex(ValueError, 'Ambiguous'):
            q.execute(p, restore=True)

    def test_legacy_manifest_discovers_archive_ancestor(self):
        p = self.plan()
        del p['archive_root']
        q.execute(p)
        q.execute(p, restore=True)
        self.assertEqual(self.lrf.read_bytes(), b'proxy contents')

    def test_recovery_substitution_before_move_stops_without_moving_media(self):
        p = self.plan()
        moved_directory = self.base/'retained-recovery'
        real_move = q.exclusive_move

        def substitute(src, dst, **handles):
            self.assertIsNotNone(handles.get('destination_fd'))
            self.recovery.rename(moved_directory)
            Path(dst).parent.mkdir(parents=True)
            return real_move(src, dst, **handles)

        with mock.patch.object(q, 'exclusive_move', side_effect=substitute):
            with self.assertRaisesRegex(ValueError, 'retained-recovery'):
                q.execute(p)
        self.assertEqual(self.lrf.read_bytes(), b'proxy contents')
        self.assertTrue((moved_directory/'manifest.json').is_file())
        self.assertFalse(Path(p['entries'][0]['destination']).exists())

    def test_recovery_substitution_after_move_keeps_media_with_manifest(self):
        p = self.plan()
        original_state = self.state.read_bytes()
        retained = self.base/'retained-recovery'
        destination = Path(p['entries'][0]['destination'])
        real_sync = os.fsync
        substituted = False

        def substitute(fd):
            nonlocal substituted
            if not substituted and not self.lrf.exists() and destination.exists():
                self.recovery.rename(retained)
                destination.parent.mkdir(parents=True)
                substituted = True
            return real_sync(fd)

        with mock.patch.object(q.os, 'fsync', side_effect=substitute):
            with self.assertRaisesRegex(ValueError, 'retained-recovery'):
                q.execute(p)
        self.assertTrue(substituted)
        self.assertEqual((retained/'files'/self.lrf.name).read_bytes(), b'proxy contents')
        self.assertTrue((retained/'manifest.json').is_file())
        self.assertTrue((retained/'restore_lrf.py').is_file())
        self.assertFalse(destination.exists())
        self.assertEqual(self.state.read_bytes(), original_state)

    def test_artifact_creation_uses_retained_recovery_directory(self):
        p = self.plan()
        retained = self.base/'retained-recovery'
        real_artifact = q.artifact
        changed = False

        def substitute(path, data, **kwargs):
            nonlocal changed
            if Path(path).name == 'manifest.json' and kwargs.get('create', True) and not changed:
                self.recovery.rename(retained)
                self.recovery.mkdir()
                changed = True
            return real_artifact(path, data, **kwargs)

        with mock.patch.object(q, 'artifact', side_effect=substitute):
            with self.assertRaisesRegex(ValueError, 'Directory changed'):
                q.execute(p)
        self.assertEqual(self.lrf.read_bytes(), b'proxy contents')
        self.assertTrue((retained/'manifest.json').is_file())
        self.assertFalse((self.recovery/'manifest.json').exists())

    def test_restore_refuses_replaced_recovery_parent(self):
        p = self.plan()
        q.execute(p)
        retained = self.base/'retained-recovery'
        real_move = q.exclusive_move

        def substitute(src, dst, **handles):
            self.recovery.rename(retained)
            Path(src).parent.mkdir(parents=True)
            return real_move(src, dst, **handles)

        with mock.patch.object(q, 'exclusive_move', side_effect=substitute):
            with self.assertRaisesRegex(ValueError, 'retained-recovery'):
                q.execute(p, restore=True)
        self.assertFalse(self.lrf.exists())
        self.assertEqual((retained/'files'/self.lrf.name).read_bytes(), b'proxy contents')


if __name__ == '__main__':
    unittest.main()
