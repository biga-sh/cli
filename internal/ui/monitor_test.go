package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestFormatChangesCollapsesLongCheckpoints(t *testing.T) {
	changes := make([]Change, 8)
	for i := range changes {
		changes[i] = Change{Path: "f" + string(rune('a'+i)), Kind: ChangeKind(i % 3)}
	}
	out := ansi.Strip(formatChanges(unicodeSymbols, "api", changes, time.Date(2026, 1, 1, 9, 5, 7, 0, time.UTC)))
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != maxListedChanges+1 {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "09:05:07  api  + fa") || !strings.Contains(lines[1], "~ fb") || !strings.Contains(lines[2], "− fc") {
		t.Errorf("unexpected lines:\n%s", out)
	}
	if !strings.Contains(lines[len(lines)-1], "… 3 more files") {
		t.Errorf("last line = %q", lines[len(lines)-1])
	}
}

func TestPlainMonitor(t *testing.T) {
	var b strings.Builder
	u := NewPlain(&b)
	m := u.Monitor(t.Context(), []Project{{Name: "api", URL: "http://x/session/0", Dir: "/code/api"}})

	m.Progress(0, "Uploading")                        // not shown off a terminal
	m.Started(0, "Started api", "http://x/session/1") // the server's link wins
	m.Live()
	m.Synced(0, []Change{{Path: "main.go", Kind: Modified}}, 120)
	m.Online(0, false)
	m.Online(0, true)

	out := b.String()
	for _, want := range []string{"◇  Started api", "Share: http://x/session/1", "Watching /code/api", "~ main.go", "Server unreachable", "Back online."} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Uploading") {
		t.Error("progress was printed off a terminal")
	}

	done := make(chan struct{})
	go func() { m.Wait(); close(done) }()
	m.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not end Wait")
	}
}

func TestPlainMonitorEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := NewPlain(&strings.Builder{}).Monitor(ctx, []Project{{Name: "a"}})
	cancel()
	m.Wait() // returns because ctx is done
}

func newTestModel(names ...string) *liveModel {
	m := &liveModel{sym: unicodeSymbols}
	for _, n := range names {
		m.projects = append(m.projects, projectState{Project: Project{Name: n, URL: "http://x/session/" + n}})
	}
	return m
}

func view(m *liveModel) string { return ansi.Strip(m.View().Content) }

func TestLiveModelLifecycle(t *testing.T) {
	m := newTestModel("api", "web")

	m.Update(progressMsg{0, "Uploading 12 files · 1.0 KB / 4.0 KB"})
	if v := view(m); !strings.Contains(v, "api  Uploading 12 files") || !strings.Contains(v, "web  Starting…") || !strings.Contains(v, "q cancel") {
		t.Errorf("starting view:\n%s", v)
	}

	if _, cmd := m.Update(startedMsg{0, "Started api", ""}); cmd == nil {
		t.Error("a started project should be printed above the panel")
	}
	m.Update(startedMsg{1, "Started web", ""})
	m.Update(liveMsg{})
	if v := view(m); !strings.Contains(v, "http://x/session/api") || !strings.Contains(v, "waiting for changes") || !strings.Contains(v, "c copy links") {
		t.Errorf("live view:\n%s", v)
	}

	m.Update(syncedMsg{0, []Change{{Path: "a.go", Kind: Added}, {Path: "b.go", Kind: Modified}}, 2048, time.Now()})
	if v := view(m); !strings.Contains(v, "1 checkpoint · 2 files · 2.0 KB sent · last sync just now") {
		t.Errorf("after sync:\n%s", v)
	}

	m.Update(onlineMsg{1, false})
	if v := view(m); !strings.Contains(v, "reconnecting") {
		t.Errorf("offline project not shown as reconnecting:\n%s", v)
	}

	m.Update(noticeMsg("Links copied"))
	if v := view(m); !strings.Contains(v, "Links copied") {
		t.Errorf("notice not shown:\n%s", v)
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil || !m.quitting {
		t.Fatal("q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should return tea.Quit")
	}
	if v := view(m); strings.Contains(v, "copy link") {
		t.Errorf("final view still shows key hints:\n%s", v)
	}
}

func TestLiveModelTruncatesToWidth(t *testing.T) {
	m := newTestModel(strings.Repeat("long-project-name", 5))
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	m.Update(startedMsg{0, "ok", ""})
	for _, line := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(line); w > 30 {
			t.Errorf("line is %d columns wide: %q", w, ansi.Strip(line))
		}
	}
}
