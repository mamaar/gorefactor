package refactor

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// writeThreadContextWorkspace writes files under a temp module directory,
// without type-checking or loading a workspace — the caller decides how to
// load it, since the two engine-level tests below need different
// EngineConfig.SkipCompilation values.
func writeThreadContextWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	_, ok := files["go.mod"]
	if !ok {
		files["go.mod"] = "module example.com/threadtest\n\ngo 1.21\n"
	}

	for rel, content := range files {
		full := filepath.Join(dir, rel)

		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}

		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	return dir
}

// TestThreadContext_EngineExecutePlanCompiles is the single most important
// test in this package's context-threading support: it is the only one that
// proves a ThreadContext plan can be applied through the *engine* (not just
// built and rendered in memory, as thread_context_test.go and
// thread_context_render_test.go do) and that the result actually compiles.
// SkipCompilation is deliberately false here — createEngine in
// tests/helpers_test.go always sets it true, which would make this test pass
// even if the rendered output were broken Go — and an explicit `go build
// ./...` is run afterward as a second, independent proof that does not rely
// on the engine's own compilation check.
func TestThreadContext_EngineExecutePlanCompiles(t *testing.T) {
	dir := writeThreadContextWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func Process(name string) string {
	return "processing " + name
}
`,
		"cmd/app/main.go": `package main

import (
	"fmt"

	"example.com/threadtest/foo"
)

func Run() string {
	return foo.Process("task")
}

func main() {
	fmt.Println(Run())
}
`,
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := CreateEngineWithConfig(&EngineConfig{SkipCompilation: false, AllowBreaking: true}, logger)

	ws, err := eng.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "Process",
		SourceFile:   "foo/lib.go",
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}

	if len(plan.Changes) == 0 {
		t.Fatalf("expected ThreadContext to produce changes, got none")
	}

	// ExecutePlan itself checks compilation per affected directory because
	// SkipCompilation is false; a failure here already proves the point.
	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	// Second, independent proof: build the whole module from its root. This
	// does not depend on the engine's own validateCompilation logic at all.
	cmd := exec.Command("go", "build", "./...")

	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./... failed after ThreadContext: %v\n%s", err, out)
	}

	rewritten, err := os.ReadFile(filepath.Join(dir, "foo", "lib.go"))
	if err != nil {
		t.Fatalf("reading rewritten foo/lib.go: %v", err)
	}

	if !strings.Contains(string(rewritten), "ctx context.Context") {
		t.Errorf("expected foo/lib.go to gain a ctx context.Context parameter, got:\n%s", rewritten)
	}

	rewrittenMain, err := os.ReadFile(filepath.Join(dir, "cmd", "app", "main.go"))
	if err != nil {
		t.Fatalf("reading rewritten cmd/app/main.go: %v", err)
	}

	if !strings.Contains(string(rewrittenMain), "context.TODO()") {
		t.Errorf("expected cmd/app/main.go's root (main) to pass context.TODO(), got:\n%s", rewrittenMain)
	}
}

// TestThreadContext_Engine_ThreadContextRequestInInterface confirms
// ThreadContext is reachable through the RefactorEngine interface (not just
// on the *DefaultEngine concrete type), since callers such as
// internal/mcp/context_tools.go hold a *DefaultEngine directly, but
// tests/helpers_test.go's createEngine and other callers hold the interface.
func TestThreadContext_Engine_ThreadContextRequestInInterface(t *testing.T) {
	dir := writeThreadContextWorkspace(t, map[string]string{
		"main.go": `package main

func target() {}

func main() { target() }
`,
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true, AllowBreaking: true}, logger)

	ws, err := eng.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "target",
		SourceFile:   "main.go",
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}

	if len(plan.Changes) == 0 {
		t.Fatalf("expected changes")
	}

	if len(plan.Operations) != 1 {
		t.Fatalf("expected exactly one operation on the plan, got %d", len(plan.Operations))
	}
}
