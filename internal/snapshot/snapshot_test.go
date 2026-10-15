package snapshot

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/biga-sh/cli/internal/api"
	"github.com/biga-sh/cli/internal/ignore"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func build(t *testing.T, root string) *api.FileNode {
	t.Helper()
	filter, err := ignore.NewFilter(root)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := Build(t.Context(), root, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func index(tree *api.FileNode) map[string]*api.FileNode {
	nodes := map[string]*api.FileNode{}
	var visit func(*api.FileNode)
	visit = func(n *api.FileNode) {
		nodes[n.Path] = n
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(tree)
	return nodes
}

func TestBuild(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".gitignore":              "*.log\n",
		"main.go":                 "package main\n",
		"src/app/util.go":         "package app\n",
		"debug.log":               "noise",
		"image.png":               "\x89PNG\x00\x01",
		"node_modules/x/index.js": "x",
		"dist/bundle.js":          "bundle",
		"pkg/build/out.o":         "obj",
		".env":                    "SECRET=1",
		"invalid-utf8.txt":        "ok\xffok",
	})
	big := bytes.Repeat([]byte("a"), MaxFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	tree := build(t, root)
	nodes := index(tree)

	if tree.Path != "." || tree.Type != api.NodeDirectory {
		t.Errorf("root = %+v", tree)
	}
	for _, want := range []string{"main.go", "src", "src/app", "src/app/util.go", ".gitignore", "image.png", "big.txt"} {
		if nodes[want] == nil {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{"debug.log", "node_modules", ".env", "dist/bundle.js", "pkg/build/out.o"} {
		if nodes[unwanted] != nil {
			t.Errorf("%s should be excluded", unwanted)
		}
	}

	if n := nodes["main.go"]; n.Content != "package main\n" || n.Size != 13 || len(n.Hash) != 64 || n.Binary {
		t.Errorf("main.go = %+v", n)
	}
	if n := nodes["image.png"]; !n.Binary || n.Content != "" || n.Size != 6 {
		t.Errorf("image.png = %+v", n)
	}
	if n := nodes["big.txt"]; !n.Binary || n.Content != "" || n.Size != int64(len(big)) {
		t.Errorf("big.txt = binary %v, size %d", n.Binary, n.Size)
	}
	if n := nodes["invalid-utf8.txt"]; n.Content != "ok�ok" || n.Hash != hashString(n.Content) {
		t.Errorf("invalid-utf8.txt = %q", n.Content)
	}
	for _, stub := range []string{"dist", "pkg/build"} {
		if n := nodes[stub]; n == nil || !n.Ignored || len(n.Children) != 0 {
			t.Errorf("%s should be an empty stub, got %+v", stub, n)
		}
	}
}

func TestBuildSkipsUnreadableFiles(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions are not enforced")
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"ok.txt": "ok", "locked.txt": "secret"})
	if err := os.Chmod(filepath.Join(root, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}

	filter, _ := ignore.NewFilter(root)
	var skipped []string
	tree, err := Build(t.Context(), root, filter, func(rel string, _ error) { skipped = append(skipped, rel) })
	if err != nil {
		t.Fatal(err)
	}
	nodes := index(tree)
	if nodes["locked.txt"] != nil || nodes["ok.txt"] == nil {
		t.Errorf("tree = %v", nodes)
	}
	if len(skipped) != 1 || skipped[0] != "locked.txt" {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestBuildFailsOnMissingRoot(t *testing.T) {
	filter, _ := ignore.NewFilter(t.TempDir())
	if _, err := Build(t.Context(), filepath.Join(t.TempDir(), "nope"), filter, nil); err == nil {
		t.Fatal("Build succeeded for a missing root")
	}
}

func TestDiff(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"same.txt":    "same",
		"changed.txt": "new",
		"added.txt":   "added",
	})
	tree := build(t, root)
	hashes := Hashes(tree)

	manifest := &api.Manifest{Files: []api.ManifestEntry{
		{Path: "same.txt", Hash: hashes["same.txt"]},
		{Path: "changed.txt", Hash: strings.Repeat("0", 64)},
		{Path: "removed.txt", Hash: strings.Repeat("1", 64)},
	}}

	entries := Diff(manifest, tree)
	got := map[string]api.ChangeType{}
	for _, e := range entries {
		got[e.Path] = e.ChangeType
	}
	want := map[string]api.ChangeType{
		"added.txt":   api.ChangeAdded,
		"changed.txt": api.ChangeModified,
		"removed.txt": api.ChangeDeleted,
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for path, change := range want {
		if got[path] != change {
			t.Errorf("%s: %q, want %q", path, got[path], change)
		}
	}

	upload := api.CheckpointUpload{SessionID: "s", Entries: entries, Timestamp: 1}
	if err := upload.Validate(); err != nil {
		t.Errorf("diff produced an invalid checkpoint: %v", err)
	}
}

func TestDiffInSync(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "a"})
	tree := build(t, root)
	manifest := &api.Manifest{Files: []api.ManifestEntry{{Path: "a.txt", Hash: Hashes(tree)["a.txt"]}}}
	if entries := Diff(manifest, tree); entries != nil {
		t.Errorf("Diff = %v, want nil", entries)
	}
}
