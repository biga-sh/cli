package ui

import (
	"strings"
	"testing"
)

func TestPlainOutput(t *testing.T) {
	var b strings.Builder
	u := NewPlain(&b)
	u.Intro("biga")
	u.Info("line one\nline two")
	u.Prefixed("api").Warn("careful")
	s := u.Spin("Starting")
	s.Stop("Started")
	s.Fail("ignored: a spinner ends once")
	u.Outro("Done")

	want := "┌  biga\n" +
		"│\n●  line one\n│  line two\n" +
		"│\n▲  [api] careful\n" +
		"│\n◇  Started\n" +
		"│\n└  Done\n\n"
	if got := b.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(b.String(), "\x1b[") {
		t.Error("plain output contains ANSI escapes")
	}
}
