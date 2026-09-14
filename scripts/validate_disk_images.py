#!/usr/bin/env python3
"""Validate recovery on disposable macOS APFS, exFAT, and HFS+ images.

Only creates/detaches images under its own /private/tmp directory. Artifacts are
retained. Command fixtures use real files but mocked physical-drive identities;
mounted identity tests separately exercise DiskManagement. Forced detach tests
are not physical power-loss tests.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--filesystem", choices=("APFS", "ExFAT", "HFS+"),
                        help="limit testing (ExFAT also mounts an HFS+ archive)")
    parser.add_argument("--skip-disconnect", action="store_true")
    args = parser.parse_args()
    if sys.platform != "darwin":
        raise SystemExit("Disk-image validation requires macOS")
    repo = Path(__file__).resolve().parent.parent
    work = Path(tempfile.mkdtemp(prefix="reel-filesystem-validation-", dir="/private/tmp"))
    print("Validation artifacts: {}".format(work), flush=True)

    def run(argv, log, cwd=repo, env=None):
        result = subprocess.run(argv, cwd=cwd, env=env, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT)
        (work / log).write_bytes(result.stdout)
        if result.returncode:
            print(result.stdout.decode("utf-8", errors="replace")[-6000:], file=sys.stderr)
            raise RuntimeError("{} failed ({}); see {}".format(argv[0], result.returncode, work / log))
        return result.stdout

    run(["diskutil", "info", "-plist", "/"], "host-volume.plist")
    run(["sw_vers"], "os-version.txt")
    run(["go", "version"], "go-version.txt")
    run(["git", "rev-parse", "HEAD"], "base-commit.txt")
    run(["git", "diff", "--", "."], "working-tree.patch")
    sources = list((repo / "cmd").glob("*.go")) + list((repo / "internal").rglob("*.go"))
    sources += [repo / "main.go", repo / "go.mod", repo / "go.sum", Path(__file__).resolve()]
    fingerprints = {str(p.relative_to(repo)): hashlib.sha256(p.read_bytes()).hexdigest()
                    for p in sorted(sources)}
    (work / "source-sha256.json").write_text(json.dumps(fingerprints, indent=2) + "\n")
    suites = {
        "cmd": "Test(CommandWorkflowsRestore|CommandsCollisionPreservesArchive|"
               "RestoreBindsOnlyUntransferredPreviewIdentity|"
               "TransferStateSyncFailuresPreserveMediaAndRetry|"
               "RecoveryStateSyncFailuresRetainJournalAndRetry|"
               "DryRunDoesNotMoveOrMirror|MountedRecoveryGuards)$",
        "internal/volume": "TestMountedVolume(Identity|Capabilities)$",
        "internal/lockfile": "Test(SharedReleaseKeepsOtherReadersProtected|ProcessLockHandoff|"
                             "PythonQuarantineHonorsGoArchiveLock)$",
        "internal/trash": "Test(Copy.*|MoveOnVolumeRecoverableAndCollisionFree|"
                          "RestoreRefusesCollision|RecoveryProcessInterruption|"
                          "RecoveryFailuresNeverDelete|RecoveryRootSyncFailureStopsBeforeMovement|MountedRecoveryFullVolume)$",
    }
    binaries = {}
    for package in suites:
        name = package.replace("/", "-")
        binary = work / (name + ".test")
        run(["go", "test", "-c", "-race", "-o", str(binary), "./" + package], name + "-build.log")
        binaries[package] = binary

    selected = [args.filesystem] if args.filesystem else ["APFS", "ExFAT", "HFS+"]
    filesystems = list(dict.fromkeys(selected + (["HFS+"] if "ExFAT" in selected else [])))
    mounted = {}
    outcomes = []

    def attach(filesystem, suffix):
        mount = work / (filesystem + "-mount")
        run(["hdiutil", "attach", "-nobrowse", "-mountpoint", str(mount), "-plist",
             str(work / (filesystem + ".sparseimage"))], filesystem + "-attach-" + suffix + ".plist")
        mounted[filesystem] = mount
        return mount

    try:
        for filesystem in filesystems:
            (work / (filesystem + "-mount")).mkdir()
            run(["hdiutil", "create", "-size", "256m", "-type", "SPARSE", "-fs", filesystem,
                 "-volname", "REELTEST", str(work / (filesystem + ".sparseimage"))], filesystem + "-create.log")
            attach(filesystem, "initial")
        for filesystem in filesystems:
            mount = mounted[filesystem]
            info = plistlib.loads(run(["diskutil", "info", "-plist", str(mount)], filesystem + "-info.plist"))
            if info.get("MountPoint") != str(mount):
                raise RuntimeError("Unexpected validation mountpoint")
            env = dict(os.environ, TMPDIR=str(mount), REEL_TEST_VOLUME_ROOT=str(mount),
                       REEL_TEST_HOST_ROOT=str(work))
            for package in suites:
                package_env = dict(env)
                if package == "cmd":
                    # Desktop state/staging stay on APFS; the camera and archive
                    # use independently mounted filesystems. No suite is skipped
                    # merely because exclusive rename is unavailable on a card.
                    archive = mounted["HFS+"] if filesystem == "ExFAT" else mount
                    package_env.update(TMPDIR=str(work), REEL_TEST_CARD_ROOT=str(mount),
                                       REEL_TEST_ARCHIVE_ROOT=str(archive))
                log = filesystem + "-" + package.replace("/", "-") + ".log"
                run([str(binaries[package]), "-test.v", "-test.count=1", "-test.run=^" + suites[package]],
                    log, cwd=repo / package, env=package_env)
            outcomes.append({"filesystem": info.get("FilesystemType"), "volume_uuid": info.get("VolumeUUID"),
                             "result": "workflow-tests-passed", "suites": list(suites)})
            (work / "results.json").write_text(json.dumps(outcomes, indent=2) + "\n")
            print("{}: workflow tests passed".format(filesystem), flush=True)

        if "ExFAT" in selected and not args.skip_disconnect:
            for step in ("media-move", "recovery-copy", "recovery-copied", "recovery-remove", "media-moved",
                         "restore-intent", "restore-copy", "restore-copy-sync"):
                mount = mounted["ExFAT"]
                control = work / ("disconnect-" + step)
                env = dict(os.environ, REEL_DISCONNECT_ROOT=str(mount / ("disconnect-" + step)),
                           REEL_DISCONNECT_STEP=step, REEL_DISCONNECT_CONTROL=str(control))
                argv = [str(binaries["internal/trash"]), "-test.v", "-test.run=^TestMountedRecoveryDisconnect$"]
                with (work / ("disconnect-" + step + ".log")).open("wb") as log:
                    process = subprocess.Popen(argv, env=env, stdout=log, stderr=subprocess.STDOUT)
                    try:
                        deadline = time.monotonic() + 40
                        while not Path(str(control) + ".ready").exists():
                            if process.poll() is not None or time.monotonic() > deadline:
                                raise RuntimeError("disconnect helper did not reach " + step)
                            time.sleep(0.05)
                        run(["hdiutil", "detach", "-force", str(mount)], "disconnect-" + step + "-detach.log")
                        del mounted["ExFAT"]
                        Path(str(control) + ".resume").write_text("continue\n")
                        if process.wait(timeout=30):
                            raise RuntimeError("disconnect helper failed: " + str(control) + ".log")
                    finally:
                        if process.poll() is None:
                            process.kill()
                            process.wait()
                attach("ExFAT", step)
                env["REEL_DISCONNECT_PHASE"] = "recover"
                run(argv, "disconnect-" + step + "-recover.log", env=env)
                outcomes.append({"filesystem": "exfat", "forced_detach": step, "result": "recovery-and-retry-passed"})
                (work / "results.json").write_text(json.dumps(outcomes, indent=2) + "\n")
                print("exFAT forced detach at {}: recovery and retry passed".format(step), flush=True)
    finally:
        for filesystem, mount in list(mounted.items()):
            run(["hdiutil", "detach", str(mount)], filesystem + "-detach.log")
    print("Validation images detached. Results: {}".format(work / "results.json"), flush=True)


if __name__ == "__main__":
    main()
