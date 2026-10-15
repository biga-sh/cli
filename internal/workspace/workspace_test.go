package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspace(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissing(t *testing.T) {
	ws, err := Load(t.TempDir())
	if err != nil || ws != nil {
		t.Fatalf("Load = %v, %v; want nil, nil", ws, err)
	}
}

func TestLoadResolvesProjects(t *testing.T) {
	dir := t.TempDir()
	writeWorkspace(t, dir, `{"workspaceId":"ws-fixed","projects":[{"dir":"api","title":"API"},{"dir":"./web"}]}`)

	ws, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ID != "ws-fixed" {
		t.Errorf("ID = %q", ws.ID)
	}
	want := []Project{
		{RootDir: filepath.Join(dir, "api"), Title: "API"},
		{RootDir: filepath.Join(dir, "web")},
	}
	if len(ws.Projects) != len(want) {
		t.Fatalf("Projects = %+v", ws.Projects)
	}
	for i := range want {
		if ws.Projects[i] != want[i] {
			t.Errorf("Projects[%d] = %+v, want %+v", i, ws.Projects[i], want[i])
		}
	}
}

func TestLoadWritesGeneratedID(t *testing.T) {
	dir := t.TempDir()
	original := "{\n  \"$schema\": \"x\",\n  \"projects\": [{ \"dir\": \"a\" }]\n}\n"
	writeWorkspace(t, dir, original)

	ws, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ws.ID, "ws-") {
		t.Fatalf("ID = %q, want a generated ws- id", ws.ID)
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("rewritten file is not valid JSON: %v\n%s", err, raw)
	}
	if parsed["workspaceId"] != ws.ID || parsed["$schema"] != "x" {
		t.Errorf("rewritten file = %s", raw)
	}

	again, err := Load(dir)
	if err != nil || again.ID != ws.ID {
		t.Errorf("reload ID = %q, %v; want stable %q", again.ID, err, ws.ID)
	}
}

func TestLoadRejectsInvalidFiles(t *testing.T) {
	tests := map[string]string{
		"not json":       `{`,
		"no projects":    `{"projects":[]}`,
		"empty dir":      `{"projects":[{"dir":""}]}`,
		"wrong type":     `{"projects":[{"dir":3}]}`,
		"duplicate dirs": `{"projects":[{"dir":"a"},{"dir":"./a"}]}`,
		"empty id":       `{"workspaceId":"","projects":[{"dir":"a"}]}`,
		"unsafe id":      `{"workspaceId":"../x","projects":[{"dir":"a"}]}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeWorkspace(t, dir, content)
			if _, err := Load(dir); err == nil {
				t.Fatal("Load succeeded, want an error")
			}
		})
	}
}
