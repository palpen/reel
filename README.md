# reel

Camera transfer CLI for macOS. Moves footage from card to laptop to external HD, verifies every copy with SHA-256, and won't touch the card until you've confirmed a hash-verified backup exists.

```
$ reel status
Camera: connected (47 files, 142.3 GB)
Laptop: /Users/.../Videos/Footage — 0 new, 312 archived
HD:     connected, 312 backed up, 0 stale verifications
Last import: 2026-05-09  14:22

$ reel import
[01/47] DJI_20260516141822_0001_D.MP4  3.1GB  ████████  100%
...
✓ 47 copied, 0 skipped, 142.3 GB in 4m12s

$ reel backup
✓ 47 backed up, all hashes match

$ reel clean
Held back (47 files):
  DJI_20260516141822_0001_D.LRF  reason: not tracked in state
...
Nothing to delete.
```

MP4-only transfers leave unbacked companion files on the card. The sibling safety
check also holds their MP4s back from cleaning.

## Commands

| Command | What it does |
|---|---|
| `reel import` | Camera → laptop |
| `reel backup` | Laptop → external HD |
| `reel direct_backup` | Camera → HD (skip laptop) |
| `reel verify` | Re-hash HD files, refresh verification timestamp |
| `reel clean` | Soft-delete from camera (only after verified HD copy) |
| `reel status` | Show state of everything |
| `reel history` | Show recent activity timeline (imports, backups, verifies, cleans) |
| `reel config` | Re-run the setup wizard with current values pre-filled |

`clean` supports `--dry-run`. `status` supports `--json`.

## Safety

Transfers (`import`, `backup`, and `direct_backup`) default to MP4 only, including
files tracked by older versions. Set the top-level `"transfer_extensions": ["MP4"]`
in `~/.config/reel/config.json` to make this explicit. Add `"WAV"` if you want separate
audio too. An empty list disables all transfers. Matching is case-insensitive.
The camera profile's regex still recognizes companion files; its `extensions`
field does not control transfers.

Excluded files already on backup drives are not automatically removed. Existing
WAV backups are kept. Camera cleaning still requires every sibling to have its own
verified backup: skipping LRF/WAV can therefore hold the whole clip on the card.
Changing transfer preferences never makes an unbacked file safe to delete.

### Recoverable removal of existing LRF backups

`scripts/lrf_quarantine.py` (Python 3.9+, macOS) has separate `plan`, `apply`, and
`restore` steps. It only selects DJI `.LRF` files with a nonempty matching `.MP4`
in the same folder. Use a quarantine directory outside the footage folder, on the
same drive. Run with reel idle.

```bash
python3 scripts/lrf_quarantine.py plan \
  --root "/Volumes/MyDrive/Footage" \
  --quarantine "/Volumes/MyDrive/Reel LRF Recovery/session-1" \
  --manifest /tmp/lrf-plan.json \
  --state "$HOME/.config/reel/state.jsonl" \
  --state "/Volumes/MyDrive/.reel-state.jsonl" \
  --lock "$HOME/.config/reel/reel.lock"
# Inspect the manifest before applying. Only include state files that exist.
python3 scripts/lrf_quarantine.py apply /tmp/lrf-plan.json
```

The quarantine contains a durable manifest, original state snapshots, a standalone
restore script, and `RESTORE.txt`. Moves preserve file identity and refuse overwrites
or cross-volume operations. The tool verifies size, modification time, and inode;
it does not re-hash the video contents. It checks that other files in the backup
folder stayed unchanged. Tracked LRF backup paths are cleared while historical
timestamps and other rows are preserved. Restoration merges those paths back and
refuses conflicting state edits. Interrupted runs can be resumed or restored using
the same manifest. Changes to recorded files require review before proceeding.

Quarantined files have no automatic expiry and still occupy disk space. Nothing in
this tool permanently deletes footage. It does not scan disconnected drives, cloud
backups, or Time Machine snapshots.

`reel clean` runs eight independent checks before touching anything on the card:

1. File has an HD path recorded in state
2. HD copy was verified at least once
3. HD file physically exists on disk
4. HD file size matches what was recorded at backup time
5. HD file SHA-256 matches the canonical hash from the original transfer
6. Verification is fresh (within 7 days, or use `--force-stale`)
7. Camera path is recorded in state
8. All sibling files (MP4 + LRF + WAV for the same clip) pass every check above

Any check that fails → that file stays on the card. Files held back are listed with a reason. Nothing is silently skipped.

When the camera and your Trash are on the same disk, deletes are soft: files go to `~/.Trash/reel-deleted-<timestamp>/`, recoverable from Finder for 30 days. Camera cards mount as a separate volume, where moving to the Trash is impossible — there `reel` deletes permanently. That is safe because nothing is deleted until it has passed all eight checks against a verified HD backup (to keep the Trash safety net anyway, copy footage to an internal-disk folder first, or set `soft_delete` accordingly).

## Install

Requires Go 1.22+ and macOS.

```bash
git clone https://github.com/palpen/reel ~/projects/reel
cd ~/projects/reel
make install
```

The binary lands in `~/.local/bin/reel`. First run of any command starts an interactive config wizard.

## Upgrade

```bash
cd ~/projects/reel
git pull
make install
```

## Camera profiles

Ships with a DJI Pocket 3 profile. Other cameras (GoPro, Sony, Canon, etc.) are added by editing `~/.config/reel/config.json` — no rebuild needed.

A profile tells reel how to detect the camera volume, where DCIM files live, and how to parse filenames into a stable clip ID and recording timestamp.

```json
{
  "name":             "DJI Pocket 3",
  "volume_pattern":   "DJI*",
  "dcim_subdir":      "DCIM",
  "filename_regex":   "^(?P<base>DJI_(?P<ts>\\d{14})_\\d{4}_[A-Z])\\.(?P<ext>MP4|LRF|WAV)$",
  "timestamp_source": "filename",
  "timestamp_group":  "ts",
  "timestamp_format": "20060102150405",
  "extensions":       ["MP4", "LRF", "WAV"],
  "raw_extensions":   ["LRF", "WAV"]
}
```

If you plug in a camera reel doesn't recognize, `reel status` tells you the volume name and points you at the config docs.

## State

Everything reel knows lives in `~/.config/reel/state.jsonl` — one JSON object per line, one per `(camera_profile, base_name, extension)`. Human-readable, Time Machine-backed, not iCloud-synced.

After every mutating command, a copy is written to `<hd_root>/.reel-state.jsonl` as a disaster-recovery mirror. If the laptop dies, the HD carries both the footage and the state.

## Uninstall

```bash
make uninstall          # removes the binary
rm -rf ~/.config/reel   # removes state and config (optional)
```

## License

MIT
