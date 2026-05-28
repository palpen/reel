package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pspenano/reel/internal/config"
	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
)

type historyEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Filename  string    `json:"filename"`
}

type historyOut struct {
	Events         []historyEvent `json:"events"`
	TotalAvailable int            `json:"total_available"`
	Shown          int            `json:"shown"`
}

// RunHistory implements `reel history`.
func RunHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max events to show")
	jsonOut := fs.Bool("json", false, "emit structured JSON")
	typeFilter := fs.String("type", "", "filter by event type (import|backup|verify|clean)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *typeFilter != "" {
		switch *typeFilter {
		case "import", "backup", "verify", "clean":
		default:
			return fmt.Errorf("invalid --type %q: must be one of import, backup, verify, clean", *typeFilter)
		}
	}

	_, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfgDir, err := config.Dir()
	if err != nil {
		return err
	}
	lk, err := lockfile.AcquireShared(filepath.Join(cfgDir, "reel.lock"))
	if err != nil {
		return err
	}
	defer lk.Release()

	st, err := state.Load(filepath.Join(cfgDir, "state.jsonl"))
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	events := collectEvents(st)
	events = filterEvents(events, *typeFilter)

	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.After(events[j].Timestamp)
	})

	total := len(events)
	if total == 0 {
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(historyOut{Events: []historyEvent{}})
		}
		display.Print("No history yet.")
		return nil
	}

	shown := events
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(historyOut{
			Events:         shown,
			TotalAvailable: total,
			Shown:          len(shown),
		})
	}

	for _, ev := range shown {
		fmt.Printf("%-15s %-10s %s\n", display.Relative(ev.Timestamp), ev.Type, ev.Filename)
	}
	if total > len(shown) {
		fmt.Printf("\nShowing %d of %d events. Use --limit N for more.\n", len(shown), total)
	}
	return nil
}

func collectEvents(st *state.Store) []historyEvent {
	var events []historyEvent
	for _, r := range st.All() {
		filename := r.BaseName + "." + r.Ext
		if r.ImportedAt != nil {
			events = append(events, historyEvent{Timestamp: *r.ImportedAt, Type: "imported", Filename: filename})
		}
		if r.BackedUpAt != nil {
			events = append(events, historyEvent{Timestamp: *r.BackedUpAt, Type: "backed up", Filename: filename})
		}
		if r.HDVerifiedAt != nil && (r.BackedUpAt == nil || !r.HDVerifiedAt.Equal(*r.BackedUpAt)) {
			events = append(events, historyEvent{Timestamp: *r.HDVerifiedAt, Type: "verified", Filename: filename})
		}
		if r.CleanedAt != nil {
			events = append(events, historyEvent{Timestamp: *r.CleanedAt, Type: "cleaned", Filename: filename})
		}
	}
	return events
}

func filterEvents(events []historyEvent, typeFilter string) []historyEvent {
	if typeFilter == "" {
		return events
	}
	wanted := map[string]string{
		"import": "imported",
		"backup": "backed up",
		"verify": "verified",
		"clean":  "cleaned",
	}[typeFilter]
	out := events[:0]
	for _, ev := range events {
		if ev.Type == wanted {
			out = append(out, ev)
		}
	}
	return out
}
