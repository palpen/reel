import json
from pathlib import Path
import tempfile
import unittest

import lrf_quarantine as q


class QuarantineTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name).resolve()
        self.root = self.base/'backup'
        self.root.mkdir()
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
        with self.assertRaises(ValueError):
            self.plan()

    def test_symlink_aborts_plan(self):
        (self.root/'link.LRF').symlink_to(self.lrf)
        with self.assertRaises(ValueError):
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


if __name__ == '__main__':
    unittest.main()
