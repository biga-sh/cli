package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", WithUserAgent("biga-test"), WithBackoff(func(int) time.Duration { return time.Millisecond }))
}

func TestEnsureSessionUnwrapsEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/sessions/abc" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != "biga-test" {
			t.Errorf("User-Agent = %q", got)
		}
		var in CreateSessionInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title != "demo" {
			t.Errorf("body = %+v, %v", in, err)
		}
		io.WriteString(w, `{"ok":true,"data":{"id":"abc","title":"demo","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","endedAt":null}}`)
	})

	s, err := c.EnsureSession(t.Context(), "abc", "", CreateSessionInput{Title: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "abc" || s.Status != "active" {
		t.Errorf("session = %+v", s)
	}
}

func TestEnsureSessionAcceptsBareResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"abc","title":"t","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","endedAt":null}`)
	})
	s, err := c.EnsureSession(t.Context(), "abc", "", CreateSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "abc" {
		t.Errorf("ID = %q", s.ID)
	}
}

func TestStatusErrorUsesEnvelopeMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"ok":false,"error":{"code":"inactive_session","message":"Session has ended."}}`)
	})
	err := c.Heartbeat(t.Context(), "abc", "")
	se, ok := errors.AsType[*StatusError](err)
	if !ok {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.StatusCode != http.StatusConflict || se.Code != "inactive_session" || se.Message != "Session has ended." {
		t.Errorf("StatusError = %+v", se)
	}
	if !IsPermanent(err) || !HasStatus(err, http.StatusNotFound, http.StatusConflict) {
		t.Error("409 should be a permanent, matchable status")
	}
}

func TestRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.EndSession(t.Context(), "abc", ""); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})
	if err := c.EndSession(t.Context(), "abc", ""); !HasStatus(err, http.StatusBadRequest) {
		t.Fatalf("err = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if err := c.EndSession(t.Context(), "abc", ""); !HasStatus(err, http.StatusTooManyRequests) {
		t.Fatalf("err = %v", err)
	}
	if got := calls.Load(); got != defaultAttempts {
		t.Errorf("calls = %d, want %d", got, defaultAttempts)
	}
}

func TestStopsRetryingWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusBadGateway)
	})
	if err := c.EndSession(ctx, "abc", ""); err == nil {
		t.Fatal("expected an error")
	}
}

func TestManifestUnavailableOn404(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	if _, err := c.Manifest(t.Context(), "abc", ""); !errors.Is(err, ErrManifestUnavailable) {
		t.Fatalf("err = %v, want ErrManifestUnavailable", err)
	}
}

func TestSessionIDIsPathEscaped(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/sessions/a%2Fb/heartbeat" {
			t.Errorf("path = %q", r.URL.EscapedPath())
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Heartbeat(t.Context(), "a/b", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPushCheckpointValidatesLocally(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid checkpoint reached the server")
	})
	_, err := c.PushCheckpoint(t.Context(), "", &CheckpointUpload{
		SessionID: "abc",
		Entries:   []CheckpointEntry{{Path: "../etc/passwd", ChangeType: ChangeDeleted}},
		Timestamp: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("err = %v, want a traversal error", err)
	}
}

func TestCheckpointEntryJSON(t *testing.T) {
	hash := strings.Repeat("a", 64)
	tests := []struct {
		name  string
		entry CheckpointEntry
		want  string
	}{
		{
			"text",
			CheckpointEntry{Path: "a.txt", ChangeType: ChangeModified, Hash: &hash, Size: 2, Content: "hi"},
			`{"path":"a.txt","changeType":"modified","hash":"` + hash + `","size":2,"content":"hi"}`,
		},
		{
			"binary",
			CheckpointEntry{Path: "a.png", ChangeType: ChangeAdded, Hash: &hash, Size: 9, Binary: true},
			`{"path":"a.png","changeType":"added","hash":"` + hash + `","size":9,"binary":true}`,
		},
		{
			"deleted",
			CheckpointEntry{Path: "a.txt", ChangeType: ChangeDeleted},
			`{"path":"a.txt","changeType":"deleted","hash":null,"size":0}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.entry)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestFileNodeJSON(t *testing.T) {
	tree := &FileNode{
		Type: NodeDirectory, Name: "root", Path: ".",
		Children: []*FileNode{
			{Type: NodeFile, Name: "empty.txt", Path: "empty.txt", Hash: "h"},
			{Type: NodeDirectory, Name: "dist", Path: "dist", Ignored: true},
		},
	}
	got, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"directory","name":"root","path":".","children":[` +
		`{"type":"file","name":"empty.txt","path":"empty.txt","size":0,"hash":"h","content":""},` +
		`{"type":"directory","name":"dist","path":"dist","children":[],"ignored":true}]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func validUpload() *CheckpointUpload {
	hash := strings.Repeat("a", 64)
	return &CheckpointUpload{
		SessionID: "abc",
		Entries: []CheckpointEntry{{
			Path: "a.tsx", ChangeType: ChangeAdded, Hash: &hash,
			// Big enough to be worth compressing.
			Content: "<App a={1} && b />" + strings.Repeat("\n// padding", 200),
		}},
		Timestamp: 1,
	}
}

// readBody decodes a request body, gunzipping it when it is compressed.
func readBody(t *testing.T, r *http.Request) (string, bool) {
	t.Helper()
	var reader io.Reader = r.Body
	gzipped := r.Header.Get("Content-Encoding") == "gzip"
	if gzipped {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("bad gzip body: %v", err)
		}
		reader = zr
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), gzipped
}

func TestCompressesOnceServerAdvertisesGzip(t *testing.T) {
	var gzipped []bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.Header().Set("Accept-Encoding", "br, gzip;q=1")
			io.WriteString(w, `{"ok":true,"data":{"id":"abc","title":"t","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","endedAt":null}}`)
			return
		}
		body, gz := readBody(t, r)
		gzipped = append(gzipped, gz)
		if !strings.Contains(body, "<App a={1} && b />") {
			t.Errorf("content was escaped or mangled: %s", body)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	// Before the server has advertised support, bodies go out plain.
	if _, err := c.PushCheckpoint(t.Context(), "", validUpload()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureSession(t.Context(), "abc", "", CreateSessionInput{}); err != nil {
		t.Fatal(err)
	}
	if !c.Compressing() {
		t.Fatal("client did not pick up the advertised encoding")
	}
	if _, err := c.PushCheckpoint(t.Context(), "", validUpload()); err != nil {
		t.Fatal(err)
	}
	if len(gzipped) != 2 || gzipped[0] || !gzipped[1] {
		t.Errorf("gzipped = %v, want [false true]", gzipped)
	}
}

func TestFallsBackWhenCompressionIsRejected(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Content-Encoding") == "gzip" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	c.gzip.Store(true)
	if _, err := c.PushCheckpoint(t.Context(), "", validUpload()); err != nil {
		t.Fatal(err)
	}
	if c.Compressing() || calls.Load() != 2 {
		t.Errorf("compressing = %v after %d calls; want plain retry", c.Compressing(), calls.Load())
	}
}

func TestHonorsRetryAfter(t *testing.T) {
	var first time.Time
	var gap time.Duration
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		gap = time.Since(first)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.EndSession(t.Context(), "abc", ""); err != nil {
		t.Fatal(err)
	}
	if gap < 900*time.Millisecond {
		t.Errorf("retried after %v, want about 1s", gap)
	}
}

func TestUnreachableServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now

	c := New("http://"+addr, WithBackoff(func(int) time.Duration { return time.Millisecond }))
	err = c.Heartbeat(t.Context(), "abc", "")
	if !IsUnreachable(err) {
		t.Fatalf("err = %v, want an UnreachableError", err)
	}
	if !strings.Contains(err.Error(), "cannot reach http://"+addr) || strings.Contains(err.Error(), "dial tcp") {
		t.Errorf("message = %q, want a short cause", err)
	}
}

func TestUploadSnapshotReportsProgress(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength <= 0 {
			t.Errorf("ContentLength = %d, want a fixed length", r.ContentLength)
		}
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	snap := &FileSnapshot{SessionID: "abc", Tree: &FileNode{
		Type: NodeDirectory, Name: "r", Path: ".",
		Children: []*FileNode{{Type: NodeFile, Name: "a", Path: "a", Hash: "h", Content: strings.Repeat("x", 100_000)}},
	}, Timestamp: 1}

	var last, total int
	sent, err := c.UploadSnapshot(t.Context(), "", snap, func(s, t int) { last, total = s, t })
	if err != nil {
		t.Fatal(err)
	}
	if last != total || total != sent || sent < 100_000 {
		t.Errorf("progress %d/%d, sent %d", last, total, sent)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("3", now); got != 3*time.Second {
		t.Errorf("seconds: %v", got)
	}
	if got := parseRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now); got != 5*time.Second {
		t.Errorf("date: %v", got)
	}
	if got := parseRetryAfter("soon", now); got != 0 {
		t.Errorf("garbage: %v", got)
	}
}

func TestSmallBodiesAreNotCompressed(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "" {
			t.Error("a tiny body was compressed")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	c.gzip.Store(true)
	small := validUpload()
	small.Entries[0].Content = "x"
	if _, err := c.PushCheckpoint(t.Context(), "", small); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsAreSent(t *testing.T) {
	var auth, key atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		key.Store(r.Header.Get("X-Biga-Key"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, WithAccessKey("bgk_key"))
	if err := c.Heartbeat(t.Context(), "abc", "bgs_token"); err != nil {
		t.Fatal(err)
	}
	if auth.Load() != "Bearer bgs_token" || key.Load() != "bgk_key" {
		t.Errorf("Authorization = %q, X-Biga-Key = %q", auth.Load(), key.Load())
	}

	if err := New(srv.URL).Heartbeat(t.Context(), "abc", ""); err != nil {
		t.Fatal(err)
	}
	if auth.Load() != "" || key.Load() != "" {
		t.Errorf("credentials sent without being configured: %q %q", auth.Load(), key.Load())
	}
}

func TestEnsureSessionReturnsToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"ok":true,"data":{"id":"abc","title":"t","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","endedAt":null,"token":"bgs_x","shareUrl":"https://biga.example/session/abc"}}`)
	})
	s, err := c.EnsureSession(t.Context(), "abc", "", CreateSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Token != "bgs_x" || s.ShareURL != "https://biga.example/session/abc" {
		t.Errorf("session = %+v", s)
	}
}

func TestOwnershipAndAccessKeyErrors(t *testing.T) {
	for _, tc := range []struct {
		status         int
		code           string
		ownership, key bool
	}{
		{http.StatusUnauthorized, "unauthorized", true, false},
		{http.StatusForbidden, "forbidden", true, false},
		{http.StatusForbidden, "session_locked", true, false},
		{http.StatusUnauthorized, "access_key_required", false, true},
		{http.StatusUnauthorized, "access_key_invalid", false, true},
		{http.StatusConflict, "invalid_state", false, false},
	} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, `{"ok":false,"error":{"code":"`+tc.code+`","message":"m"}}`)
		})
		err := c.EndSession(t.Context(), "abc", "bgs_x")
		if IsOwnershipError(err) != tc.ownership || IsAccessKeyError(err) != tc.key {
			t.Errorf("%d %s: ownership=%v key=%v", tc.status, tc.code, IsOwnershipError(err), IsAccessKeyError(err))
		}
		if !IsPermanent(err) {
			t.Errorf("%d %s should not be retried", tc.status, tc.code)
		}
	}
}
