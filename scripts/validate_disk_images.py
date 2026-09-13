#!/usr/bin/env python3
"""Run preservation fixtures on new disposable macOS APFS and exFAT images.

Requires DiskManagement access. Uses only images/mountpoints it creates under
/private/tmp, detaches them on exit, and retains images and logs for inspection.
Camera/archive identities in command fixtures remain mocked; the volume test
separately checks the real mounted image's identity. This is not a power-loss test.
"""

import argparse
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--filesystem", choices=("APFS", "ExFAT"),
                        help="run only one filesystem (default: both)")
    args = parser.parse_args()
    if sys.platform != "darwin":
        raise SystemExit("Disk-image validation requires macOS")
    repo = Path(__file__).resolve().parent.parent
    work = Path(tempfile.mkdtemp(prefix="reel-filesystem-validation-", dir="/private/tmp"))
    print("Validation artifacts: {}".format(work), flush=True)

    def run(args, log, cwd=repo, env=None):
        result = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT)
        (work / log).write_bytes(result.stdout)
        if result.returncode:
            print(result.stdout.decode("utf-8", errors="replace")[-4000:], file=sys.stderr)
            raise RuntimeError("{} failed ({}); see {}".format(args[0], result.returncode, work / log))
        return result.stdout

    run(["diskutil", "info", "-plist", "/"], "host-volume.plist")
    run(["sw_vers"], "os-version.txt")
    run(["go", "version"], "go-version.txt")
    run(["git", "rev-parse", "HEAD"], "base-commit.txt")
    run(["git", "diff", "--", "."], "working-tree.patch")
    suites = {
        "cmd": "Test(CommandWorkflowsRestore|CommandsCollisionPreservesArchive|"
               "RestoreBindsOnlyUntransferredPreviewIdentity|"
               "TransferStateSyncFailuresPreserveMediaAndRetry|"
               "RecoveryStateSyncFailuresRetainJournalAndRetry)$",
        "internal/volume": "TestMountedVolume(Identity|Capabilities)$",
        "internal/lockfile": "Test(SharedReleaseKeepsOtherReadersProtected|ProcessLockHandoff|"
                             "PythonQuarantineHonorsGoArchiveLock)$",
    }
    binaries = {}
    for package in suites:
        name = package.replace("/", "-")
        binary = work / (name + ".test")
        run(["go", "test", "-c", "-race", "-o", str(binary), "./" + package], name + "-build.log")
        binaries[package] = binary

    outcomes = []
    for filesystem in ([args.filesystem] if args.filesystem else ("APFS", "ExFAT")):
        image = work / (filesystem + ".sparseimage")
        mount = work / (filesystem + "-mount")
        mount.mkdir()
        run(["hdiutil", "create", "-verbose", "-size", "256m", "-type", "SPARSE", "-fs", filesystem,
             "-volname", "REELTEST", str(image)], filesystem + "-create.log")
        attached = False
        try:
            run(["hdiutil", "attach", "-nobrowse", "-mountpoint", str(mount), "-plist", str(image)],
                filesystem + "-attach.plist")
            attached = True
            info = plistlib.loads(run(["diskutil", "info", "-plist", str(mount)], filesystem + "-info.plist"))
            if info.get("MountPoint") != str(mount):
                raise RuntimeError("Unexpected validation mountpoint")
            env = dict(os.environ, TMPDIR=str(mount), REEL_TEST_VOLUME_ROOT=str(mount),
                       REEL_TEST_HOST_ROOT=str(work))
            executed = []
            capabilities = None
            for package in ("internal/volume", "internal/lockfile", "cmd"):
                if package == "cmd" and not capabilities["exclusive_rename"]:
                    continue
                log = filesystem + "-" + package.replace("/", "-") + ".log"
                run([str(binaries[package]), "-test.v", "-test.count=1", "-test.run=^" + suites[package]],
                    log, cwd=repo / package, env=env)
                executed.append(package)
                if package == "internal/volume":
                    capabilities = json.loads((mount / "capabilities.json").read_text())
            result = ("workflow-tests-passed" if capabilities["exclusive_rename"]
                      else "unsupported-exclusive-rename-safe-rejection-passed")
            outcomes.append({"filesystem": info.get("FilesystemType"), "volume_uuid": info.get("VolumeUUID"),
                             "result": result, "suites": executed})
            (work / "results.json").write_text(json.dumps(outcomes, indent=2) + "\n")
            print("{}: {}".format(filesystem, result), flush=True)
        finally:
            if attached:
                run(["hdiutil", "detach", str(mount)], filesystem + "-detach.log")
    print("Validation images detached. Results: {}".format(work / "results.json"), flush=True)


if __name__ == "__main__":
    main()
