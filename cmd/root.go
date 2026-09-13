// Package cmd implements the reel CLI commands.
package cmd

import (
	"errors"
	"flag"
	"fmt"
	"github.com/pspenano/reel/internal/config"
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
  clean         Move verified camera files into recoverable storage
  restore       Restore a camera file using its recovery journal
  status        Show current state of camera, laptop, and HD
  history       Show recent activity (imports, backups, verifies, cleans)
  config        Edit the reel configuration (paths and volume identity)

Global flags:
  --version     Print version and exit
  --help        Print this help

Run 'reel <command> --help' for command-specific flags.
`

// Run dispatches to the appropriate command and returns an exit code.
func Run(args []string, version string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, helpText)
		return 2
	}
	if err := validateArguments(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(helpText)
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
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
			return commandExit(err)
		}
		return 0
	case "backup":
		if err := RunBackup(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel backup: %v\n", err)
			return commandExit(err)
		}
		return 0
	case "direct_backup":
		if err := RunDirectBackup(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel direct_backup: %v\n", err)
			return commandExit(err)
		}
		return 0
	case "verify":
		if err := RunVerify(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel verify: %v\n", err)
			return commandExit(err)
		}
		return 0
	case "restore":
		if err := RunRestore(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return commandExit(err)
		}
		return 0
	case "clean":
		if err := RunClean(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel clean: %v\n", err)
			return commandExit(err)
		}
		return 0
	case "status":
		if err := RunStatus(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel status: %v\n", err)
			return commandExit(err)
		}
		return 0
	case "history":
		if err := RunHistory(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel history: %v\n", err)
			return commandExit(err)
		}
		return 0
	case "config":
		if err := RunConfig(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "reel config: %v\n", err)
			return commandExit(err)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "reel: unknown command %q\n\n%s", args[0], helpText)
		return 2
	}
}

func commandExit(err error) int {
	if errors.Is(err, config.ErrInvalid) {
		return 2
	}
	return 1
}
