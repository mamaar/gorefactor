package refactor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseGoWorkFile_BlockSyntax(t *testing.T) {
	content := []byte(`go 1.21

use (
	./moduleA
	./moduleB
	./internal/moduleC
)
`)

	dirs := parseGoWorkFile(content)
	if len(dirs) != 3 {
		t.Fatalf("expected 3 dirs, got %d: %v", len(dirs), dirs)
	}

	expected := []string{"./moduleA", "./moduleB", "./internal/moduleC"}
	for i, e := range expected {
		if dirs[i] != e {
			t.Errorf("dirs[%d] = %q, want %q", i, dirs[i], e)
		}
	}
}

func TestParseGoWorkFile_SingleLineSyntax(t *testing.T) {
	content := []byte(`go 1.21

use ./moduleA
use ./moduleB
`)

	dirs := parseGoWorkFile(content)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}

	if dirs[0] != "./moduleA" || dirs[1] != "./moduleB" {
		t.Errorf("unexpected dirs: %v", dirs)
	}
}

func TestParseGoWorkFile_MixedSyntax(t *testing.T) {
	content := []byte(`go 1.21

use ./standalone

use (
	./grouped1
	./grouped2
)
`)

	dirs := parseGoWorkFile(content)
	if len(dirs) != 3 {
		t.Fatalf("expected 3 dirs, got %d: %v", len(dirs), dirs)
	}
}

func TestParseGoWorkFile_WithComments(t *testing.T) {
	content := []byte(`go 1.21

// this is a comment
use (
	// also a comment
	./moduleA
	./moduleB
)
`)

	dirs := parseGoWorkFile(content)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}
}

func TestParseGoWorkFile_Empty(t *testing.T) {
	content := []byte(`go 1.21
`)

	dirs := parseGoWorkFile(content)
	if len(dirs) != 0 {
		t.Fatalf("expected 0 dirs, got %d: %v", len(dirs), dirs)
	}
}

func TestParseGoWorkFile_NoSpaceBlock(t *testing.T) {
	content := []byte(`go 1.21

use(
	./moduleA
)
`)

	dirs := parseGoWorkFile(content)
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d: %v", len(dirs), dirs)
	}
}

func TestParseModuleName(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "simple",
			content: "module github.com/foo/bar\n\ngo 1.21\n",
			want:    "github.com/foo/bar",
		},
		{
			name:    "with version",
			content: "module github.com/foo/bar\n\ngo 1.21\n\nrequire (\n)\n",
			want:    "github.com/foo/bar",
		},
		{
			name:    "no module line",
			content: "go 1.21\n",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseModuleName([]byte(tt.content))
			if got != tt.want {
				t.Errorf("parseModuleName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscoverWorkspaceModules(t *testing.T) {
	// Set up a temp directory with go.work and two module directories.
	tmpDir := t.TempDir()

	// Create go.work
	goWork := `go 1.21

use (
	./moduleA
	./moduleB
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.work"), []byte(goWork), 0644); err != nil {
		t.Fatal(err)
	}

	// Create moduleA/go.mod
	if err := os.MkdirAll(filepath.Join(tmpDir, "moduleA"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "moduleA", "go.mod"), []byte("module github.com/test/moduleA\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create moduleB/go.mod
	if err := os.MkdirAll(filepath.Join(tmpDir, "moduleB"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "moduleB", "go.mod"), []byte("module github.com/test/moduleB\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}

	modules, err := discoverWorkspaceModules(filepath.Join(tmpDir, "moduleA"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(modules) != 2 {
		t.Fatalf("expected 2 modules, got %d: %v", len(modules), modules)
	}

	found := map[string]bool{}
	for _, m := range modules {
		found[m] = true
	}

	if !found["github.com/test/moduleA"] || !found["github.com/test/moduleB"] {
		t.Errorf("unexpected modules: %v", modules)
	}
}

func TestDiscoverWorkspaceModules_NoGoWork(t *testing.T) {
	tmpDir := t.TempDir()

	modules, err := discoverWorkspaceModules(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modules != nil {
		t.Errorf("expected nil modules when no go.work, got %v", modules)
	}
}

func TestDiscoverWorkspaceModules_MissingGoMod(t *testing.T) {
	tmpDir := t.TempDir()

	goWork := `go 1.21

use ./nomod
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.work"), []byte(goWork), 0644); err != nil {
		t.Fatal(err)
	}

	// Create directory without go.mod
	if err := os.MkdirAll(filepath.Join(tmpDir, "nomod"), 0755); err != nil {
		t.Fatal(err)
	}

	modules, err := discoverWorkspaceModules(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(modules) != 0 {
		t.Errorf("expected 0 modules, got %d: %v", len(modules), modules)
	}
}
