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
Will move to Trash from camera:
  47 MP4 files (142.3 GB)
  47 LRF files  (8.1 GB)
All have verified HD copies (most recent verify: 14s ago).
Delete 94 files from camera? [y/N]: y
✓ moved to ~/.Trash/reel-deleted-2026-05-16T19-04-22/
```

Four commands. One `y`. Card cleared.

## Commands

| Command | What it does |
|---|---|
| `reel import` | Camera → laptop |
| `reel backup` | Laptop → external HD |
| `reel direct_backup` | Camera → HD (skip laptop) |
| `reel verify` | Re-hash HD files, refresh verification timestamp |
| `reel clean` | Soft-delete from camera (only after verified HD copy) |
| `reel status` | Show state of everything |

All transfer and delete commands support `--dry-run`. `status` supports `--json`.

## Safety

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

First-version deletes are soft: files go to `~/.Trash/reel-deleted-<timestamp>/`, recoverable from Finder for 30 days.

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
