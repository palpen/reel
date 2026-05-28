package display

import (
	"testing"
	"time"
)

func TestRelative(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"future is just now", now.Add(5 * time.Minute), "just now"},
		{"sub-minute is just now", now.Add(-30 * time.Second), "just now"},
		{"1 minute ago", now.Add(-1 * time.Minute), "1 minute ago"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"59 minutes ago", now.Add(-59 * time.Minute), "59 minutes ago"},
		{"1 hour ago", now.Add(-1 * time.Hour), "1 hour ago"},
		{"2 hours ago", now.Add(-2 * time.Hour), "2 hours ago"},
		{"23 hours ago", now.Add(-23 * time.Hour), "23 hours ago"},
		{"yesterday", now.Add(-30 * time.Hour), "yesterday"},
		{"yesterday upper bound", now.Add(-47 * time.Hour), "yesterday"},
		{"2 days ago", now.Add(-48 * time.Hour), "2 days ago"},
		{"3 days ago", now.Add(-3 * 24 * time.Hour), "3 days ago"},
		{"1 week ago", now.Add(-7 * 24 * time.Hour), "1 week ago"},
		{"2 weeks ago", now.Add(-14 * 24 * time.Hour), "2 weeks ago"},
		{"3 weeks ago", now.Add(-21 * 24 * time.Hour), "3 weeks ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Relative(tt.t)
			if got != tt.want {
				t.Errorf("Relative(%v) = %q, want %q", tt.t, got, tt.want)
			}
		})
	}
}

func TestRelativeAbsoluteFallback(t *testing.T) {
	t.Run("greater than 30 days renders as YYYY-MM-DD", func(t *testing.T) {
		old := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
		got := Relative(old)
		want := old.Local().Format("2006-01-02")
		if got != want {
			t.Errorf("Relative(%v) = %q, want %q", old, got, want)
		}
	})
}
