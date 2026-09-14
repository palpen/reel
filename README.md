# reel

Camera transfer CLI for macOS. Copies footage from card to laptop to external HD,
verifies copies with SHA-256, and moves backed-up camera files into journaled
recovery after confirmation. Recovery retains the media on the card and does not
free card space.

APFS and HFS+ archives support transfer, verification, and metadata operations.
exFAT camera cards support recoverable clean and restore through verified copies.
Publishing new archive media to exFAT still requires unsupported exclusive rename
and is refused. See [filesystem support and validation](#filesystems-and-release-validation).

On supported filesystems, the usual workflow is:

```bash
reel config          # First-time setup; bind the mounted backup drive
reel import          # Camera → laptop; MP4 only by default
reel backup          # Laptop → backup drive
reel verify          # Recheck archived files
reel clean --dry-run # Preview eligible recovery moves
reel clean           # Confirm moves into the camera's .reel-trash directory
```

Use `reel direct_backup` instead of `import` and `backup` to copy straight from the
camera to the backup drive. Connect exactly one camera for camera operations.

MP4-only transfers work with camera cleaning: a verified MP4 allows its matching
LRF preview to be cleared too. Unbacked WAV audio stays on the card.

## Commands

| Command | What it does |
|---|---|
| `reel import` | Camera → laptop |
| `reel backup` | Laptop → external HD |
| `reel direct_backup` | Camera → HD (skip laptop) |
| `reel verify` | Re-hash HD files, refresh verification timestamp |
| `reel clean` | Move camera files into recovery after independent backup verification |
| `reel restore --journal PATH` | Restore a camera recovery entry without overwriting |
| `reel status` | Show state of everything |
| `reel history` | Show recent activity timeline (imports, backups, verifies, cleans) |
| `reel config` | Create or edit configuration and bind the mounted backup drive |

| Command | Flags |
|---|---|
| `clean` | `--dry-run`; `--force-stale` bypasses only the seven-day verification age check |
| `verify` | `--bind-legacy` binds verified backup records to the selected drive; `--scope hd` is the only supported scope |
| `restore` | `--journal /absolute/path/to/recovery.json` is required |
| `status` | `--json` |
| `history` | `--json`; `--limit N` (default 20; zero shows all); `--type TYPE` accepts `import`, `backup`, `verify`, or `clean` |

`reel status` reports HD copies as absent, unverified, conflicting, stale, or
verified, and lists retained staging and transfer records. It hashes tracked HD
files, so checking a large archive can take time. `history` reports timestamps
from current state rather than a complete log of every command invocation.

## Safety

Transfers (`import`, `backup`, and `direct_backup`) default to MP4 only, including
files tracked by older versions. Set the top-level `"transfer_extensions": ["MP4"]`
in `~/.config/reel/config.json` to make this explicit. Add `"WAV"` if you want separate
audio too, or `"LRF"` to include previews. An empty list disables all transfers.
Matching is case-insensitive.
The camera profile's regex still recognizes companion files; its `extensions`
field does not control transfers.

Excluded files already on backup drives are not automatically removed. Existing
WAV backups are kept. Camera cleaning verifies originals independently. An LRF
preview can be cleared when the current MP4 in the same camera folder passes all
backup checks; a separate LRF backup is unnecessary. Orphan LRFs stay on the card.
WAV audio requires its own verified backup and is held back independently, without
blocking the MP4. Transfer exclusions never authorize deleting original audio.

### Recoverable removal of existing LRF backups

`scripts/lrf_quarantine.py` (Python 3.9+, macOS) has separate `plan`, `apply`, and
`restore` steps. It only selects DJI `.LRF` files with a nonempty matching `.MP4`
in the same folder. Use a quarantine directory outside the managed archive, on the
same drive. A selected `--root` can be a subtree: the tool discovers the unique
managed ancestor from `.reel-protocol.json` and locks that ancestor, matching Go.
For an archive with no marker, specify `--archive-root` explicitly. Conflicting or
nested markers are rejected rather than choosing a lock arbitrarily.

```bash
python3 scripts/lrf_quarantine.py plan \
  --root "/Volumes/MyDrive/Footage" \
  --archive-root "/Volumes/MyDrive/Footage" \
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
or cross-volume operations. The tool verifies content hashes, size, modification time, and inode. It checks that other files in the backup
folder stayed unchanged. Tracked LRF backup paths are cleared while historical
timestamps and other rows are preserved. Restoration merges those paths back and
refuses conflicting state edits. Interrupted runs can be resumed or restored using
the same manifest. Legacy manifests without an archive marker require an explicit
root when applying or restoring, for example:

```bash
python3 scripts/lrf_quarantine.py restore /path/to/manifest.json \
  --archive-root "/Volumes/MyDrive/Footage"
```

Artifacts and media moves use retained directory handles. If a recovery directory
is replaced during an operation, Reel stops and reports the retained location;
it never moves recovery data into the replacement directory. Changes to recorded
files require review before proceeding.

Quarantined files have no automatic expiry and still occupy disk space. Nothing in
this tool permanently deletes footage. It does not scan disconnected drives, cloud
backups, or Time Machine snapshots.

### Camera cleaning and restoration

`reel clean` checks each original MP4/WAV before removing it from the camera folder:

1. File has an HD path recorded in state
2. HD copy was verified at least once
3. HD file physically exists on disk
4. HD file size matches what was recorded at backup time
5. HD file SHA-256 matches the canonical hash from the original transfer
6. Verification is fresh (within 7 days, or use `--force-stale`)
7. The current camera path matches the recorded original; source and backup are regular files without symlink paths and are not the same file
8. The current camera file size and SHA-256 match the original transfer (protecting against reused filenames)
9. Original and backup content are hashed again after confirmation and immediately before movement
10. The backup is inside the approved root on the bound volume, on independent physical storage; unresolved identities fail closed

Any check that fails → that file stays on the card. Files held back are listed with a reason. Nothing is silently skipped.

Cleaning is recoverable-only. Files move into unique folders under
`<camera-volume>/.reel-trash/`. Each folder contains the media and a `recovery.json`
record with its original path and hash. These folders have no automatic expiry. Restore with
`reel restore --journal "/Volumes/Camera/.reel-trash/<entry>/recovery.json"`.
Restoration refuses destination conflicts, including case-insensitive collisions.
On exFAT, clean creates the recovery file exclusively, flushes and independently
verifies it, revalidates the camera and backup, and only then removes the camera
entry. This needs temporary free space for one file. Partial copies and journals
are retained on failure; retrying clean uses a fresh recovery folder.
exFAT restore creates the destination exclusively and **keeps the recovery copy**.
An interrupted restore can append the missing suffix only when its durable
`restore.json` receipt identifies that file and every existing byte matches recovery.
A replaced file, changed prefix, or missing/damaged receipt is a conflict. In the
small creation-before-receipt interruption window, preserve the destination under
another name before retrying; Reel will not guess that an unowned file is disposable.
In Finder, press Cmd+Shift+. to show the hidden recovery folder.
Journal restoration also works on a fresh installation: it creates the local lock
directory without requiring a configuration file or connected backup drive.
For an LRF first tracked during cleaning, restore records its verified journal hash
so it can later be imported or backed up when LRF transfers are enabled. If an older
build already restored that LRF, rerun the same restore command to repair its state.
The same journal can also be retried after media restoration succeeds but saving
local state fails. Reel checks the already-restored bytes against the journal
before repairing the state.

Reel never falls back to permanent deletion. Mutations require supported filesystem capabilities; see the validation limits below.
Recovered files still occupy card space until the recovery folder is removed or
moved off the card. Reel excludes `.reel-trash` from camera scanning. Preview files
are moved first; any file operation or state-saving failure stops the run.

Legacy `soft_delete: false`, `null`, and invalid values are rejected before mutation.
Remove the obsolete setting or set it to `true`; omission also selects recoverable
cleaning. There is no permanent flag, purge command, or automatic expiry.
`--force-stale` only bypasses the age check. `reel clean --dry-run` does not move media,
create recovery records, run setup, or mirror state. Unsupported positional arguments
are errors, including `reel clean camera-name --dry-run`. Connect exactly one camera
for camera mutations.

### Transfers and retries

Transfers never replace existing archives. An identical destination can be reused
after current source and destination verification; differing content stops the batch.
Before the copy engine returns success, it rechecks that the destination still
names the verified file and that its containing directory has not been replaced.
This also applies when reusing an existing identical copy or accepting a matching
copy published by another writer; an unchanged volume UUID alone is insufficient.

A backup skip requires the recorded path to be inside the selected archive and
its volume UUID to match. A copy on another drive causes an explicit conflict; the
existing single-backup history is preserved. Unbound legacy records require
`reel verify --bind-legacy`. Matching, bound records with missing copies are recreated
from a verified source; changed canonical originals and ambiguous filenames require review.
Removing a completed laptop copy does not block later backups: Reel checks that
the recorded archive still matches its canonical hash, size, and selected volume.
An unavailable or conflicting archive still stops the batch.

Interrupted `.reel-stage-*` media and `.reel-transfer-*` intents remain in place.
Legacy `.tmp` files are never swept. No automatic recovery cleanup occurs.
After fixing an operational failure, rerun the transfer command: completed copies
are checked and remaining files are retried. Retained partials are not automatically
resumed or removed. If media was published before a state write failed, the retry
can reuse the matching destination and record the completed copy.

Exit codes: `0` means completed work or a legitimate empty selection; `1` means an
operational failure, partial failure, or cancellation; `2` means invalid arguments or
configuration. Transfer retries repair the state mirror even when there is no media
to copy, including when the camera has been disconnected or emptied. A failed mirror
write on a connected drive returns an error. Plain import can work with the optional
backup drive disconnected.

## Install

Requires Go 1.22+, macOS, and a C toolchain (Xcode Command Line Tools).
The Darwin exclusive rename wrapper uses cgo. A non-cgo build refuses media publication.

```bash
git clone https://github.com/palpen/reel ~/projects/reel
cd ~/projects/reel
make install
```

The binary lands in `~/.local/bin/reel`. Run `reel config` explicitly to create configuration and bind the mounted backup drive’s UUID. Read-only commands and dry runs never launch setup.

## Upgrade

```bash
cd ~/projects/reel
git pull
make install
```

State schema 2 retains legacy fields and writes a durable `.reel-pre-migration-*`
snapshot before modifying schema 1 data. Malformed or duplicate records stop the
operation. Reads do not migrate. Existing backup paths remain in place. After binding
the intended drive with `reel config`, run `reel verify --bind-legacy` to verify and
bind legacy backup records before cleaning. Do not run older Reel binaries against
these archives: old releases do not honor the new locking and preservation protocol.

## Filesystems and release validation

Reel supports APFS, HFS+, and exFAT for locking and metadata. APFS/HFS+ media
publication and recovery moves require a successful exclusive-rename probe.
exFAT recoverable cleaning explicitly uses a copy/verify/remove protocol, recorded
in version 2 recovery journals. It never falls back from a failed rename. Recovery
journals and copies require file/directory sync and macOS `F_FULLFSYNC`; errors stop
source removal. New media publication to an exFAT archive remains unavailable.
`.reel-capability-*` directories contain owned empty probe files. Keep lockfiles in
place; unlinking them breaks coordination. Network filesystems are unsupported.

The fixture suites cover eligibility, collisions, aliases, file and directory
replacement, interrupted copies, process locking, state failures, and retries.
The disposable-image harness runs complete workflows with an exFAT camera and an
HFS+ archive, tests actual full-volume failure, and force-detaches/reattaches its
exFAT image at recovery and restore checkpoints. Image tests do not establish
physical independence of drives or physical power-loss durability.

Run `python3 scripts/validate_disk_images.py` on macOS with DiskManagement access.
`--filesystem ExFAT` includes an HFS+ archive; APFS and HFS+ can also be selected.
Images and logs are retained under `/private/tmp`, and the harness detaches its
images on exit. See [exFAT/HFS+ validation](docs/EXFAT_RECOVERY.md) and the
[earlier remediation record](docs/REMEDIATION_VALIDATION.md).

Run the local checks with:

```bash
make test           # Go race tests and Python tests
go vet ./...
make build
```

## Camera profiles

Ships with a DJI Pocket 3 profile. Other cameras (GoPro, Sony, Canon, etc.) are added by editing `~/.config/reel/config.json` — no rebuild needed.

A profile tells reel how to detect the camera volume, where DCIM files live, and how to parse filenames into a stable clip ID and recording timestamp.

```json
{
  "name":             "DJI Pocket 3",
  "volume_name":      "DJI Pocket 3",
  "media_path":       "DCIM",
  "filename_regex":   "^(?P<base>DJI_(?P<ts>\\d{14})_\\d{4}_[A-Z])\\.(?P<ext>MP4|LRF|WAV)$",
  "timestamp_source": "filename",
  "timestamp_group":  "ts",
  "timestamp_format": "20060102150405",
  "extensions":       ["MP4", "LRF", "WAV"],
  "raw_extensions":   ["LRF", "WAV"]
}
```

Camera and HD subdirectories must be relative and cannot contain `..`. Symlinks, media hard links, and unexpected device changes are rejected.

## State

Everything reel knows lives in `~/.config/reel/state.jsonl` — one JSON object per line, one per `(camera_profile, base_name, extension)`. Human-readable, Time Machine-backed, not iCloud-synced.

After transfer, verification, or cleaning, compatible rows are merged to `<hd_root>/.reel-state.jsonl` under a persistent lock as a disaster-recovery mirror. If the laptop dies, the HD carries both the footage and the state.

Writers use persistent locks for local state, archives, and camera operations. The
Python quarantine tool shares the archive lock with Go, including for subtree
selections. Keep lockfiles and `.reel-protocol.json` markers in place. Unknown state
fields are preserved, while malformed records, duplicate keys, and conflicting
canonical identities stop writes.

The current supported use case is one camera and one desktop. Independently
configured desktops sharing an archive can still lose client-local history when
merging records for the same recording; that limitation remains unresolved.

## Uninstall

```bash
make uninstall          # removes the binary
rm -rf ~/.config/reel   # removes state and config (optional)
```

## License

MIT
