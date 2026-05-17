// Package cmd implements the reel CLI commands.
package cmd

import (
	"fmt"
	"os"
)

const helpText = `reel — camera file transfer manager

Usage:
  reel <command> [flags]

Commands:
  import        Copy files from camera to laptop
  backup        Copy files from laptop to HD
  direct_backup Copy files from camera directly to HD
  verify        Re-hash HD files to verify integrity
  clean         Delete camera files that have been safely backed up
  status        Show current state of camera, laptop, and HD

Global flags:
  --version     Print version and exit
  --help        Print this help

Run 'reel <command> --help' for command-specific flags.
`

// Run dispatches to the appropriate command and returns an exit code.
func Run(args []string, version string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, helpText)
		return 1
	}

	switch args[0] {
	case "--version", "-version":
		fmt.Println("reel " + version)
		return 0
	case "--help", "-help", "help":
		fmt.Print(helpText)
		return 0
	case "import":
		if err := RunImport(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel import: %v\n", err)
			return 1
		}
		return 0
	case "backup":
		if err := RunBackup(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel backup: %v\n", err)
			return 1
		}
		return 0
	case "direct_backup":
		if err := RunDirectBackup(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel direct_backup: %v\n", err)
			return 1
		}
		return 0
	case "verify":
		if err := RunVerify(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel verify: %v\n", err)
			return 1
		}
		return 0
	case "clean":
		if err := RunClean(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel clean: %v\n", err)
			return 1
		}
		return 0
	case "status":
		if err := RunStatus(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel status: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "reel: unknown command %q\n\n%s", args[0], helpText)
		return 1
	}
}
