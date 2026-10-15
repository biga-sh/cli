package human

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	tests := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"}
	for n, want := range tests {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCount(t *testing.T) {
	if got := Count(1, "file", "files"); got != "1 file" {
		t.Errorf("Count(1) = %q", got)
	}
	if got := Count(0, "file", "files"); got != "0 files" {
		t.Errorf("Count(0) = %q", got)
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	tests := map[time.Duration]string{
		time.Second:      "just now",
		12 * time.Second: "12s ago",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	}
	for d, want := range tests {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("Ago(-%v) = %q, want %q", d, got, want)
		}
	}
}
