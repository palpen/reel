// Package display handles progress output to stderr and summaries to stdout.
package display

import (
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// isTTY returns true if the given file descriptor is connected to a terminal.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// Progress writes a progress message to stderr.
// If stderr is a TTY, it uses \r to overwrite the previous line.
// Otherwise, it emits a plain newline.
func Progress(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if isTTY(os.Stderr) {
		fmt.Fprintf(os.Stderr, "\r%-80s", msg)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
}

// ClearProgress clears the current progress line (TTY only).
func ClearProgress() {
	if isTTY(os.Stderr) {
		fmt.Fprintf(os.Stderr, "\r%80s\r", "")
	}
}

// Info writes an informational message to stderr with a newline.
func Info(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// Print writes a message to stdout.
func Print(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

// PrintTo writes a message to a writer.
func PrintTo(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format+"\n", args...)
}

// Error writes an error message to stderr.
func Error(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
}

// Warn writes a warning message to stderr.
func Warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warn: "+format+"\n", args...)
}

// Relative formats t as a human-readable duration relative to now.
// Examples: "just now", "5 minutes ago", "2 hours ago", "yesterday",
// "3 days ago", "2 weeks ago". For ages > ~30 days, falls back to an
// absolute date like "2026-04-12".
func Relative(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d / time.Minute)
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d / time.Hour)
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
	case d < 30*24*time.Hour:
		w := int(d / (7 * 24 * time.Hour))
		if w == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", w)
	default:
		return t.Local().Format("2006-01-02")
	}
}

// Bytes formats a byte count as a human-readable string.
func Bytes(n int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
