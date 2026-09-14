# exFAT recovery and HFS+ archives

The Go clean/restore workflow supports exFAT camera cards, including cards whose
macOS driver rejects `RENAME_EXCL`. HFS+ (`hfs`) is accepted for archive locking,
verification, transfer, and metadata persistence. The MP4/LRF/WAV eligibility
predicate is unchanged. This supersedes the exFAT clean blocker in the earlier
[remediation validation record](REMEDIATION_VALIDATION.md). The separate Python
quarantine helper has not gained exFAT media-move support.

## Recovery protocol

APFS and HFS+ retain exclusive native moves. exFAT selects a separate protocol
from the filesystem type, before media mutation; no rename error triggers a
fallback. Archive publication still requires exclusive rename, so new publication
to an exFAT backup archive remains refused.

For each eligible file, clean:

1. Retains no-follow source, card, and directory handles and checks the device.
2. Exclusively creates a random recovery directory beneath `.reel-trash`.
3. Writes an immutable version 2 `recovery.json`, containing `verified-copy`,
   original/recovery paths, size, SHA-256, and original inode/creation time.
4. Syncs the journal and containing directories, including macOS `F_FULLFSYNC`.
5. Creates the recovery media with `O_CREAT|O_EXCL`, copies from the retained
   original handle, flushes it and its directory, and independently hashes it.
6. Revalidates the command's source and backup snapshots and bound volumes after
   copying, checks retained directory identities and both named files, then removes
   the original directory entry and syncs its parent.

Only the camera directory entry is removed, after a durable verified recovery copy
exists. No recovery media is deleted or truncated. An error stops the batch;
failed/partial copies and their journals stay available for inspection. A clean
retry creates a fresh recovery entry. Local state is updated only after movement,
and state-save failure cannot remove recovery data. The existing local, archive,
and camera locks, independent-drive checks, confirmation, and post-confirmation
revalidation remain in place. Dry-run performs no recovery copy, source removal,
journal creation, history update, or mirror write.

The protocol needs free space for one additional file at a time. It does not free
card space. An actual full-volume failure leaves the original untouched; no
in-place or unchecked-rename workaround is attempted.

## Restore and retries

`reel restore --journal /absolute/path/to/recovery.json` recognizes both journal
versions. Version 2 restore retains the recovery file even after success. It
creates the original destination exclusively, so existing files, directories,
symlinks, and case-insensitive collisions cannot be overwritten.

A version 2 restore persists `restore.json` before copying the remaining suffix.
The receipt binds the output's inode and creation time to this journal. On exFAT,
an empty file has a temporary inode that changes on first allocation, so restore
writes one actual source byte and flushes it before recording identity. Retrying
requires the same identity and a byte-for-byte matching prefix; it only appends
missing bytes. A completed restore is independently hashed again before state
repair. This also supports retry after state-file or state-directory sync failure.

If clean was interrupted before removing its source, restore can recognize the
original using its recorded identity and hash and leave it untouched. A partial
recovery is never used to reconstruct media if its original is unavailable.

If interruption occurs between output creation and durable receipt creation, or
if the receipt is damaged, ownership cannot be established: restore refuses the
existing destination and leaves recovery intact. Preserve that destination under
a different name before retrying. Similarly, a filesystem that changes persistent
file identity on remount causes refusal rather than guessing. Recovery never expires.

## Validation

Validated on macOS 26.5.2, arm64, Go 1.26.3, with disposable 256 MiB images under
`/private/tmp`. No real camera, Seagate archive, user configuration, or installed
Reel binary was modified.

The harness runs actual filesystem operations on an exFAT camera image and an
HFS+ archive image, with desktop state/staging on the host APFS filesystem. It also
runs the workflows on APFS and HFS+ camera fixtures. Command fixture volume UUIDs
and physical-drive identities are injected; separate mounted tests exercise real
DiskManagement identity resolution and rejection of wrong/same-volume identities.
Images backed by the same physical computer do not prove drive independence.

Final exFAT/HFS+ run: `/private/tmp/reel-filesystem-validation-q3rh0pje/results.json`.
All source fingerprints match the completed code. APFS plus the additional
full-volume/collision tests also passed in
`/private/tmp/reel-filesystem-validation-iglykefw/results.json`.
All images, including the initial probes, were detached.

Local checks passed: `go test -race -count=1 ./...`, `go vet ./...`, all 19 Python
tests, native build, and `git diff --check`. The review binary is
`/private/tmp/reel-exfat-validated`; it was not installed. Remote CI and physical
camera/Seagate disconnect or power-loss tests were not run.

Coverage includes:

- Import → backup → verify → clean → restore and direct backup → verify → clean
  → restore, including restored LRF transfers and unbacked WAV retention.
- Existing and case-insensitive destination collisions; identical-byte source and
  backup replacement; recovery file and directory replacement; changed prefixes.
- Local/mirrored state sync failures and retries; clean/restore state failures;
  process lock handoff and Go/Python archive-lock interoperability on HFS+.
- Actual exFAT volume exhaustion, preserved source bytes, and retry after freeing
  only a disposable filler file; injected copy, sync, and removal errors.
- Process exits at recovery/restore transitions and append-only restoration retries.
- Forced exFAT detach with live descriptors at `media-move`, `recovery-copy`,
  `recovery-copied`, `recovery-remove`, `media-moved`, `restore-intent`,
  `restore-copy`, and `restore-copy-sync`; reattachment of the same image and
  successful byte-for-byte recovery and repeat restore.

Run `python3 scripts/validate_disk_images.py` with DiskManagement access to repeat
these tests. `--filesystem ExFAT` also mounts an HFS+ archive. The harness retains
images, source fingerprints, and logs, and detaches its own images on exit.

These tests establish process-error and mounted-image recovery behavior, not
physical power-loss durability. Device firmware must honor flush requests.
Locks coordinate cooperating Reel processes; the camera and other applications
must not write the card concurrently. Path-based identity checks detect observed
replacement, but macOS does not provide an atomic compare-inode-and-unlink call.
