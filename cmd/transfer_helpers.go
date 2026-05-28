package cmd

import (
	"os"

	"github.com/pspenano/reel/internal/display"
)

// handleTransferError centralizes per-file failure handling for the transfer commands
// (import, backup, direct_backup). If the destination or source volume has disappeared,
// it prints a clear multi-line message and returns true so the caller breaks the loop.
// Otherwise it prints the per-file error and returns false (caller continues).
//
// Caller is responsible for incrementing its own failure counter.
func handleTransferError(err error, filename, srcDir, destDir string) (abort bool) {
	display.ClearProgress()

	if !pathExists(destDir) {
		display.Error("destination volume is no longer mounted: %s", destDir)
		display.Info("  The drive may have disconnected, or the network share dropped.")
		display.Info("  Re-plug and re-run — already-copied files won't be re-done.")
		return true
	}
	if !pathExists(srcDir) {
		display.Error("source volume is no longer mounted: %s", srcDir)
		display.Info("  The camera or source disk may have disconnected.")
		display.Info("  Re-plug and re-run — already-copied files won't be re-done.")
		return true
	}
	display.Error("%s: %v", filename, err)
	return false
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
