package tests_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const facadeSourceStore = `package store

type Store struct{}

func New() *Store { return &Store{} }

const Version = "1"
`

func writeFacadeSource(t *testing.T, dir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), facadeSourceStore)
	mustWrite(t, filepath.Join(dir, "internal", "store", "store_windows.go"), "package store\n\nfunc WinOnly() {}\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store_linux.go"), "package store\n\nfunc LinuxOnly() {}\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "tagx.go"), "//go:build tagx\n\npackage store\n\nfunc TagX() {}\n")
}

func callFacadeTool(t *testing.T, dir, tool string, args map[string]any) {
	t.Helper()
	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()
	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s transport error: %v", tool, err)
	}
	if result.IsError {
		t.Fatalf("%s returned an error result: %+v", tool, result.Content)
	}
}

func generateFacades(t *testing.T, dir string) {
	t.Helper()
	callFacadeTool(t, dir, "generate_facades", map[string]any{"modules_dir": "internal", "target_dir": "api"})
}

func updateFacades(t *testing.T, dir string) {
	t.Helper()
	callFacadeTool(t, dir, "update_facades", map[string]any{"auto_detect": true})
}

func assertBuildsEverywhere(t *testing.T, dir string, tags ...string) {
	t.Helper()
	for _, goos := range []string{"linux", "darwin", "windows"} {
		args := []string{"build"}
		if len(tags) > 0 {
			args = append(args, "-tags", strings.Join(tags, ","))
		}
		cmd := exec.Command("go", append(args, "./...")...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go build under GOOS=%s tags=%v failed: %v\n%s", goos, tags, err, out)
		}
	}
}

func facadeFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "api", "store", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range matches {
		matches[i] = filepath.Base(m)
	}
	return matches
}

func TestGenerateFacadesEmitsConstrainedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFacadeSource(t, dir)
	generateFacades(t, dir)

	got := strings.Join(facadeFiles(t, dir), " ")
	want := "facade.go facade_linux.go facade_tagx_tags.go facade_windows.go"
	if got != want {
		t.Fatalf("facade files = %q, want %q", got, want)
	}
	facade := readFile(t, filepath.Join(dir, "api", "store", "facade.go"))
	if strings.Contains(facade, "WinOnly") || strings.Contains(facade, "TagX") {
		t.Errorf("constrained symbols leaked into the unconstrained facade:\n%s", facade)
	}
	tagged := readFile(t, filepath.Join(dir, "api", "store", "facade_tagx_tags.go"))
	if !strings.HasPrefix(tagged, "//go:build tagx\n") || !strings.Contains(tagged, "TagX") {
		t.Errorf("tag facade should carry the source constraint:\n%s", tagged)
	}
	assertBuildsEverywhere(t, dir)
	assertBuildsEverywhere(t, dir, "tagx")
}

func TestUpdateFacadesTracksConstrainedSymbols(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), facadeSourceStore)
	generateFacades(t, dir)

	windows := filepath.Join(dir, "internal", "store", "store_windows.go")
	mustWrite(t, windows, "package store\n\nfunc WinOnly() {}\n")
	updateFacades(t, dir)
	got := readFile(t, filepath.Join(dir, "api", "store", "facade_windows.go"))
	if !strings.Contains(got, "WinOnly") {
		t.Fatalf("update did not add the windows-only symbol:\n%s", got)
	}
	assertBuildsEverywhere(t, dir)

	err := os.Remove(windows)
	if err != nil {
		t.Fatal(err)
	}
	updateFacades(t, dir)
	got = strings.Join(facadeFiles(t, dir), " ")
	if got != "facade.go" {
		t.Fatalf("facade files after removing the last windows symbol = %q, want facade.go", got)
	}
	assertBuildsEverywhere(t, dir)
}

func TestUpdateFacadesRegeneratesInPlace(t *testing.T) {
	dir := t.TempDir()
	writeFacadeSource(t, dir)
	generateFacades(t, dir)
	custom := filepath.Join(dir, "api", "store", "facade_custom.go")
	mustWrite(t, custom, "package store\n\nfunc Helper() {}\n")

	mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), facadeSourceStore+"\nfunc Added() {}\n")
	callFacadeTool(t, dir, "update_facades", map[string]any{"facade_packages": []string{"api/store"}})

	facade := readFile(t, filepath.Join(dir, "api", "store", "facade.go"))
	n := strings.Count(facade, "package store")
	if n != 1 {
		t.Errorf("facade.go has %d package clauses, want 1:\n%s", n, facade)
	}
	if !strings.Contains(facade, "Added") {
		t.Errorf("facade.go lacks the new symbol:\n%s", facade)
	}
	got := readFile(t, custom)
	if !strings.Contains(got, "Helper") {
		t.Errorf("hand-written facade_custom.go was touched:\n%s", got)
	}
	assertBuildsEverywhere(t, dir)
}

func TestGenerateFacadesIgnoresFilesNoConfigIncludes(t *testing.T) {
	dir := t.TempDir()
	writeFacadeSource(t, dir)
	mustWrite(t, filepath.Join(dir, "internal", "store", "gen.go"), "//go:build ignore\n\npackage main\n\nfunc Generate() {}\n")
	generateFacades(t, dir)

	got := strings.Join(facadeFiles(t, dir), " ")
	want := "facade.go facade_linux.go facade_tagx_tags.go facade_windows.go"
	if got != want {
		t.Fatalf("facade files = %q, want %q", got, want)
	}
	facade := readFile(t, filepath.Join(dir, "api", "store", "facade.go"))
	if !strings.Contains(facade, "package store") {
		t.Errorf("facade.go has the wrong package clause:\n%s", facade)
	}
	assertBuildsEverywhere(t, dir)
}

func TestGenerateFacadesBuildsWhenOnlyConstrainedFilesExport(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), "package store\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store_linux.go"), "package store\n\nfunc LinuxOnly() {}\n")
	generateFacades(t, dir)

	got := strings.Join(facadeFiles(t, dir), " ")
	if got != "facade.go facade_linux.go" {
		t.Fatalf("facade files = %q, want facade.go facade_linux.go", got)
	}
	assertBuildsEverywhere(t, dir)
}

func TestUpdateFacadesDeletesStaleFilesWhenNothingIsExportedAnymore(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), "package store\n")
	linux := filepath.Join(dir, "internal", "store", "store_linux.go")
	mustWrite(t, linux, "package store\n\nfunc LinuxOnly() {}\n")
	generateFacades(t, dir)
	err := os.Remove(linux)
	if err != nil {
		t.Fatal(err)
	}
	updateFacades(t, dir)

	got := facadeFiles(t, dir)
	if len(got) != 0 {
		t.Errorf("facade files = %v, want none", got)
	}
}

func TestGenerateFacadesCgoFileGoesToCgoConstrainedFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), facadeSourceStore)
	mustWrite(t, filepath.Join(dir, "internal", "store", "native.go"), "package store\n\n// int one(void) { return 1; }\nimport \"C\"\n\nfunc Native() int { return int(C.one()) }\n")
	generateFacades(t, dir)

	var cgoFile string
	for _, name := range facadeFiles(t, dir) {
		text := readFile(t, filepath.Join(dir, "api", "store", name))
		if strings.Contains(text, "Native") {
			cgoFile = text
		}
	}
	if !strings.HasPrefix(cgoFile, "//go:build cgo\n") {
		t.Errorf("Native is not in a file constrained to cgo:\n%s", cgoFile)
	}
}
