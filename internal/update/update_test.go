package update

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"v0.4.0", "v0.5.0", true},
		{"0.4.0", "0.4.1", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.2.0", "v1.1.9", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"dev", "v9.9.9", false},
		{"v1.0.0", "", false},
	}
	for _, tt := range tests {
		if got := IsNewer(tt.current, tt.latest); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestRefreshCachesForADay(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("GitHub requires a User-Agent")
		}
		io.WriteString(w, `{"tag_name":"v0.9.0"}`)
	}))
	defer srv.Close()

	c := &Checker{Current: "v0.5.0", CachePath: filepath.Join(t.TempDir(), "update.json"), Endpoint: srv.URL}
	if _, ok := c.Available(); ok {
		t.Fatal("update reported before any check")
	}
	for range 3 {
		if err := c.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("fetched %d times, want 1", n)
	}
	if latest, ok := c.Available(); !ok || latest != "v0.9.0" {
		t.Errorf("Available() = %q, %v", latest, ok)
	}
}

func TestRefreshRejectsBadResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"tag_name":"nightly"}`)
	}))
	defer srv.Close()
	c := &Checker{Current: "v0.5.0", CachePath: filepath.Join(t.TempDir(), "update.json"), Endpoint: srv.URL}
	if err := c.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh accepted a non-semver tag")
	}
}
