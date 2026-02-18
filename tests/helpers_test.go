package tests_test

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

var update = flag.Bool("update", false, "update golden files")

// copyFixture copies a fixture directory to a temp dir, skipping .golden and .deleted files.
func copyFixture(t *testing.T, fixtureDir string) string {
	t.Helper()
	src := filepath.Join("testdata", fixtureDir)
	dst := t.TempDir()

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasSuffix(path, ".golden") || strings.HasSuffix(path, ".deleted") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyFixture(%s): %v", fixtureDir, err)
	}
	return dst
}

// createEngine creates a refactoring engine with SkipCompilation and AllowBreaking enabled.
func createEngine(t *testing.T) refactor.RefactorEngine {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return refactor.CreateEngineWithConfig(&refactor.EngineConfig{
		SkipCompilation: true,
		AllowBreaking:   true,
	}, logger)
}

// loadWorkspace loads a workspace from the given directory.
func loadWorkspace(t *testing.T, eng refactor.RefactorEngine, dir string) *types.Workspace {
	t.Helper()
	ws, err := eng.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace(%s): %v", dir, err)
	}
	return ws
}

// buildReferenceIndex builds a reference index needed by change_signature and add_context_parameter.
func buildReferenceIndex(t *testing.T, ws *types.Workspace) *analysis.ReferenceIndex {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := analysis.NewSymbolResolver(ws, logger)
	return resolver.BuildReferenceIndex()
}

// compareGoldenFiles walks the fixture dir for *.golden files and compares them
// against actual output in tmpDir. If -update is set, writes actual output to golden files.
//
// When -update is set, it walks the source (non-golden) files in the fixture dir
// and creates/updates corresponding .golden files from the tmpDir output. This
// allows initial golden file generation when no .golden files exist yet.
func compareGoldenFiles(t *testing.T, fixtureDir, tmpDir string) {
	t.Helper()
	srcDir := filepath.Join("testdata", fixtureDir)

	if *update {
		// Walk source files (non-golden, non-deleted) and create golden files from actual output.
		err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".golden") || strings.HasSuffix(path, ".deleted") {
				return nil
			}
			// Skip go.mod — not a refactoring output
			if d.Name() == "go.mod" {
				return nil
			}
			rel, _ := filepath.Rel(srcDir, path)
			actualPath := filepath.Join(tmpDir, rel)
			actual, err := os.ReadFile(actualPath)
			if err != nil {
				// File may have been deleted by the refactoring; skip.
				return nil
			}
			// Normalize temp paths before writing golden files.
			normalized := normalizeTempPaths(string(actual), tmpDir)
			goldenPath := path + ".golden"
			if err := os.WriteFile(goldenPath, []byte(normalized), 0o644); err != nil {
				t.Errorf("failed to update golden file %s: %v", goldenPath, err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking source files for update: %v", err)
		}
		return
	}

	// Normal mode: compare actual output against existing golden files.
	found := 0
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".golden") {
			return nil
		}
		found++

		rel, _ := filepath.Rel(srcDir, path)
		actualRel := strings.TrimSuffix(rel, ".golden")
		actualPath := filepath.Join(tmpDir, actualRel)

		actual, err := os.ReadFile(actualPath)
		if err != nil {
			t.Errorf("cannot read actual file %s: %v", actualPath, err)
			return nil
		}

		golden, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("cannot read golden file %s: %v", path, err)
			return nil
		}

		// Normalize temp dir paths so golden files don't embed host-specific paths.
		actualStr := normalizeTempPaths(string(actual), tmpDir)
		goldenStr := normalizeTempPaths(string(golden), tmpDir)

		if actualStr != goldenStr {
			t.Errorf("mismatch for %s:\n%s", actualRel, unifiedDiff(goldenStr, actualStr))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking golden files: %v", err)
	}
	if found == 0 {
		t.Fatal("no golden files found")
	}
}

// normalizeTempPaths replaces occurrences of the temp directory path (and
// any path that looks like a Go test temp dir) with a stable placeholder,
// so golden files don't depend on the host or run-specific paths.
func normalizeTempPaths(s, tmpDir string) string {
	if tmpDir != "" {
		s = strings.ReplaceAll(s, tmpDir, "$TMPDIR")
	}
	return s
}

// checkDeleted walks the fixture dir for *.deleted markers and asserts
// the corresponding files don't exist in tmpDir.
func checkDeleted(t *testing.T, fixtureDir, tmpDir string) {
	t.Helper()
	srcDir := filepath.Join("testdata", fixtureDir)

	filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".deleted") {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		actualRel := strings.TrimSuffix(rel, ".deleted")
		actualPath := filepath.Join(tmpDir, actualRel)
		if _, err := os.Stat(actualPath); err == nil {
			t.Errorf("expected %s to be deleted, but it still exists", actualRel)
		}
		return nil
	})
}

// unifiedDiff produces a simple line-by-line diff between two strings.
func unifiedDiff(expected, actual string) string {
	expectedLines := strings.Split(expected, "\n")
	actualLines := strings.Split(actual, "\n")

	var buf strings.Builder
	buf.WriteString("--- expected\n+++ actual\n")

	maxLen := len(expectedLines)
	if len(actualLines) > maxLen {
		maxLen = len(actualLines)
	}

	for i := 0; i < maxLen; i++ {
		var eLine, aLine string
		haveE, haveA := i < len(expectedLines), i < len(actualLines)
		if haveE {
			eLine = expectedLines[i]
		}
		if haveA {
			aLine = actualLines[i]
		}

		if haveE && haveA && eLine == aLine {
			fmt.Fprintf(&buf, " %s\n", eLine)
		} else {
			if haveE {
				fmt.Fprintf(&buf, "-%s\n", eLine)
			}
			if haveA {
				fmt.Fprintf(&buf, "+%s\n", aLine)
			}
		}
	}
	return buf.String()
}
