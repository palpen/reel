# Media-loss remediation validation

Implementation and follow-up validation date: 2026-09-13. No real camera media,
existing archives, user configuration, or installed Reel binary were modified.

## Implemented protections

| Audit finding | Code and regression coverage |
|---|---|
| Destination overwrite | All three commands preflight collisions; shared copy engine uses fresh staging, independent destination hashing, and descriptor-relative exclusive publication. Command collision fixtures and competing child-process copies preserve the old archive. |
| Wildcard partial deletion | Startup sweeping removed; legacy helper is report-only. Failed and interrupted stages remain with their transfer intents. Empty-import preservation regression. |
| Lock lifetime/state races | Persistent no-follow lockfiles; verify holds an exclusive local lock; shared archive lock and merge lock; reader/writer child-process handoff test; unique state temporaries and read-only Load. |
| Temporary aliases | Descriptor-relative no-follow traversal and exclusive staging. Parent symlink, predictable temp symlink, and media hard-link regressions. |
| Deletion default | JSON omission/true means recovery; false/null/invalid values fail. Wizard omits the setting. Permanent branch and legacy trash helper removed. |
| Ignored arguments | Central argument validation before dispatch, and per-command positional rejection. Invalid trailing dry-run tests never reach config loading. Startup logging no longer writes files. |
| Backup volume separation | Config stores mounted volume UUID; DiskManagement resolver checks mount root; cleanup requires independent physical storage. Unresolved identities stop. Device-separation unit tests and injected fixture resolvers; real drive validation pending below. |
| Containment | Shared descriptor traversal rejects symlinks and nested device changes; relative config paths reject traversal. Camera recovery, transfer, backup validation, and restore enforce boundaries. |
| Changes after hashing | Fresh content checks after confirmation and before each operation; recoverable-only moves; preserved-mtime corruption regression. |
| False successful exit | Copy, persistence, mirror, missing-file and verification errors fail. State/mirror failure command fixtures check exit 1 and retained bytes. |
| Route/state inconsistencies | Canonical hash is immutable; skips hash both current copies; missing copies are recreated; duplicate state/source names stop. Legacy state snapshot and volume rebinding preserve locations. |
| Python helper overwrite | Exclusive no-follow artifact creation, identical-only reuse, hard-link refusal, descriptor-relative media moves. Symlink/hard-link helper and metadata-conflict regressions. |

Both command workflows run on isolated fixture directories:

- import → backup → verify → clean → restore;
- direct backup → verify → clean → restore;
- MP4/WAV originals and MP4-associated LRF recovery;
- missing backup, canonical mismatch, collision, dry run, and permanent-mode rejection.

Deterministic checkpoints cover staging creation, transfer intent, mid-copy,
stage sync/close, publication, local state persistence, mirror persistence,
recovery intent, media movement, and restore. Subprocesses stop before publication, after recovery intent, after recovery movement,
and during restoration. Restart preserves staged bytes and restores journaled media.
Two independently configured subprocesses merge their mirror history, and a Python
transaction refuses the archive lock held by Go. These are process
failure tests, not power-loss tests.

## On-disk protocol and compatibility

- State schema 2 adds `hd_volume_uuid`. Schema 1 is read without mutation. The first
  write snapshots original bytes in `.reel-pre-migration-*`; existing fields,
  timestamps, and unknown fields survive. Invalid and duplicate rows are errors.
- `reel config` binds a mounted backup UUID. `reel verify --bind-legacy` independently
  hashes legacy backups at their existing paths and binds successful records.
- Managed archives contain an immutable `.reel-protocol.json` version 2 marker.
  Conflicting markers stop new Go/Python writers. **Old binaries do not understand
  this marker and cannot be made safe by it. Upgrade every cooperating client and
  stop old processes before using the new protocol.**
- Lock order is local state directories, managed archive `.reel-archive.lock`,
  camera lock (Go camera commands), per-copy media-directory `reel.lock`, then HD
  mirrored-state-directory `reel.lock`. Python uses the corresponding order and
  derives required state locks even without `--lock`. Locks are never unlinked.
- Metadata can be atomically replaced under its lock. Media is only published or
  moved with exclusive rename. No cleanup routine deletes stages or recoveries.
- Go recovery journals contain source/recovery paths, size, hash, and version;
  `reel restore --journal PATH` refuses collisions and can retry after a successful
  move whose state update failed. Restoration does not require the backup drive.
  Restoring an untransferred LRF binds its missing canonical hash to the verified
  journal, allowing later transfers. Existing canonical hashes and archive records
  remain unchanged. Repeating restore also repairs an older hashless restored row.
- For old Python manifests, standalone restore accepts the saved identity/size/mtime
  fingerprints; new manifests also contain hashes. New apply refuses conflicting
  helper artifacts. Never run an old unsafe helper merely to bypass that refusal.

## Original local validation

- macOS arm64, Go 1.26.3, cgo enabled: `go test -race -count=1 ./...`.
- `go vet ./...`.
- Python 3.9.6: `python3 -m unittest discover -s scripts -v` (12 tests).
- Native exclusive-rename capability tests on the host fixture filesystem.
- Native build `96d8aab-remediation-00fd057248a9` at `/private/tmp/reel-remediation`; no install.
  The fingerprint covers Go/Python source and module files. Binary argument-rejection checks passed.

## Merged review fixes

PR #3 is merged into PR #2 and closes the original five review findings:

- Backup skips require selected-archive containment and matching volume UUID;
  old-drive or unbound records stop explicitly without replacing history.
- Import, backup, and direct-backup retries perform mirror persistence even when
  selected media are already copied. Camera transfer retries also repair the mirror
  after the camera is disconnected, emptied, or excluded by transfer settings.
  Tests cover continued failure, repair without recopying, and import with the
  optional backup drive disconnected.
- Both directory walkers persist parent links before exposing directories for
  mutation, including directories left by a failed attempt. Fresh recovery entries
  also persist their parents. Injected sync failures preserve camera originals in
  DCIM and fail again on retry until the containing directory can be synced.
- Python resolves a unique managed archive ancestor and locks it for subtree
  selections. Unmarked archives require explicit `--archive-root`; nested or
  conflicting markers fail. Legacy manifests can supply the option at apply/restore.
- Python retains artifact and endpoint directory handles across apply/restore.
  Substitution before movement stops with originals intact; substitution after
  movement keeps the media with its manifest/helper and reports the retained path.

The Go race suite and 19 Python tests passed locally. This does not establish
physical power-loss durability or complete the mounted-drive tests.

The parent PR's full Go 1.22/current × Python 3.9/3.14 CI matrix passed:
[baseline CI run](https://github.com/palpen/reel/actions/runs/34740091588).
The merged branch also passed the full matrix:
[post-merge CI run](https://github.com/palpen/reel/actions/runs/34740852715).

## PR #2 follow-through

Local validation used macOS 26.5.2 (25F84), arm64, Go 1.26.3, and Python 3.9.6.
The Go race suite, `go vet ./...`, all 19 Python tests, native build, and
`git diff --check` passed.

- Reproduced the restored-LRF transfer failure in both complete workflows, then
  fixed restore to bind only untransferred previews to the verified journal hash.
  Tests also cover older already-restored rows, changed preview bytes, historical
  canonical hashes, and hashless records that claim an archive copy.
- Added failure injection immediately before state-file sync and after atomic
  replacement, before directory sync. Twelve transfer scenarios cover local and
  mirrored state in all three transfer commands. Four clean/restore scenarios
  verify nonzero exits, retained originals or recoveries and journals, and retry.
  State-store tests distinguish the old file before replacement from the complete
  new file visible after replacement whose durability is still uncertain.

### Disposable mounted filesystems

Run `python3 scripts/validate_disk_images.py` outside a sandbox that blocks
DiskManagement. `--filesystem APFS` or `--filesystem ExFAT` limits the run.
The harness creates 256 MiB sparse images and mountpoints under `/private/tmp`,
compiles race-enabled test binaries, records results, and detaches its own images.
Images and logs are retained for inspection.

Command fixtures use actual files on each image but inject camera/archive device
identities. Separate mounted-volume tests exercise real UUID resolution, reject
ordinary subdirectories as mount roots, and reject incorrect UUIDs or same-volume
storage separation. Image tests do not establish physical independence of drives.

The exFAT image exposed a compatibility blocker: exclusive publication returns
`operation not supported` on this macOS driver. An initial attempt to run the
complete workflows failed at this capability check; it must not be counted as an
exFAT workflow pass. The harness now separately verifies refusal before media
publication or recovery movement, and copying from exFAT into an APFS destination.
**Cleaning an exFAT camera card or publishing to an exFAT backup remains unavailable.**

The completed run recorded these results at
`/private/tmp/reel-filesystem-validation-0bruvb25/results.json`; both images detached.

| Filesystem | Result on macOS 26.5.2 |
|---|---|
| APFS | Both complete workflows, restored LRF transfers, collision preservation, all 16 command state-sync failure scenarios, real mounted identity, process lock handoff, and Go/Python locking passed. |
| exFAT | Real mounted identity and lock tests passed. Exclusive rename is unsupported; publication and recovery refused with original bytes intact. Copying the original into APFS passed. Complete exFAT workflows remain unsupported. |

## Release gates still open

The source changes are not a claim that the full release gate has passed.

1. Require `.github/workflows/safety.yml` to pass on each updated PR head. The
   original implementation and PR #3 merge passed the full matrix.
2. Resolve the exFAT exclusive-rename compatibility blocker before claiming
   supported exFAT cleaning or archive publication. Keep unsupported operations
   failing safely. Disposable images do not replace physical removable-drive tests.
3. Exercise read-only/full destinations, unmount/substitution between every journal
   transition, separate partitions of one device, APFS physical-store resolution,
   case-insensitive names, and two independently configured clients on one archive.
4. Complete disconnect tests at journal transitions on mounted images. State-file
   and state-directory sync, directory creation/retry, copy, recovery, restore,
   mirror retry, and cross-language subtree locking have deterministic coverage.
5. Physical power-loss durability remains a separate manual validation exercise.

The same-recording mirror merge can still discard client-local history when
independently configured clients share an archive. That review finding is deferred
for the current single-camera, single-desktop use case.

Do not install or release this build as fully remediated until these gates pass.
Retained partial media or a safely rejected operation is acceptable; lost bytes,
overwritten archives, or success on operational failure is not.
