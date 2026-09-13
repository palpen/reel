# Media-loss remediation validation

Implementation date: 2026-09-13. This change builds on the pre-existing uncommitted
cleaning changes. No real camera media, existing archives, user configuration, or
installed Reel binary were modified during implementation.

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
- For old Python manifests, standalone restore accepts the saved identity/size/mtime
  fingerprints; new manifests also contain hashes. New apply refuses conflicting
  helper artifacts. Never run an old unsafe helper merely to bypass that refusal.

## Completed local validation

- macOS arm64, Go 1.26.3, cgo enabled: `go test -race -count=1 ./...`.
- `go vet ./...`.
- Python 3.9.6: `python3 -m unittest discover -s scripts -v` (12 tests).
- Native exclusive-rename capability tests on the host fixture filesystem.
- Native build `96d8aab-remediation-00fd057248a9` at `/private/tmp/reel-remediation`; no install.
  The fingerprint covers Go/Python source and module files. Binary argument-rejection checks passed.

## Release gates still open

The source changes are not a claim that the full release gate has passed.

1. Run `.github/workflows/safety.yml` on Go 1.22/current and Python 3.9/current.
   Only Go 1.26.3 and Python 3.9.6 were installed in this environment.
2. Run disposable APFS **and exFAT** disk-image workflows on a macOS host with
   working DiskManagement. Here `diskutil info -plist /` failed because the
   DiskManagement framework was unavailable. No exFAT or real mounted-volume
   identity test could be performed. In particular, verify exFAT directory sync,
   flock, UUID resolution, and exclusive rename; unsupported operations must stop.
3. Exercise read-only/full destinations, unmount/substitution between every journal
   transition, separate partitions of one device, APFS physical-store resolution,
   case-insensitive names, and two independently configured clients on one archive.
4. Complete failure injection for state-sync/directory-sync and physical disconnect
   transitions on mounted images, beyond the deterministic copy, recovery, restore,
   state-save, mirror-save, and cross-language locking tests already present.
5. Physical power-loss durability remains a separate manual validation exercise.

Do not install or release this build as fully remediated until these gates pass.
Retained partial media or a safely rejected operation is acceptable; lost bytes,
overwritten archives, or success on operational failure is not.
