package cmd

import (
	"flag"
	"fmt"
	"io"
)

func validateArguments(args []string) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "clean":
		fs.Bool("dry-run", false, "")
		fs.Bool("force-stale", false, "")
	case "verify":
		fs.String("scope", "hd", "")
		fs.Bool("bind-legacy", false, "")
	case "restore":
		fs.String("journal", "", "")
	case "history":
		fs.Int("limit", 20, "")
		fs.Bool("json", false, "")
		fs.String("type", "", "")
	case "status":
		fs.Bool("json", false, "")
	case "import", "backup", "direct_backup", "config":
	case "--help", "-help", "help", "--version", "-version":
		if len(args) != 1 {
			return fmt.Errorf("unexpected arguments")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "restore" && fs.Lookup("journal").Value.String() == "" {
		return fmt.Errorf("restore requires --journal PATH")
	}
	if args[0] == "verify" && fs.Lookup("scope").Value.String() != "hd" {
		return fmt.Errorf("unsupported verification scope")
	}
	if args[0] == "history" {
		v := fs.Lookup("type").Value.String()
		if v != "" && v != "import" && v != "backup" && v != "verify" && v != "clean" {
			return fmt.Errorf("unsupported history type")
		}
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unsupported positional arguments: %v", fs.Args())
	}
	return nil
}
