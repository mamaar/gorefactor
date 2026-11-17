package analyzers_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/modernize"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/refactor"
)

// runModernize writes files plus a go.mod with the given go directive, type-checks
// the workspace and runs a over it.
func runModernize(t *testing.T, goDirective string, files map[string]string, a *analysis.Analyzer) []analysis.Diagnostic {
	t.Helper()

	dir := t.TempDir()
	files["go.mod"] = "module example.com/m\n\n" + goDirective + "\n"

	for rel, content := range files {
		full := filepath.Join(dir, rel)

		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	engine := refactor.CreateEngineWithConfig(&refactor.EngineConfig{SkipCompilation: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	engine.(*refactor.DefaultEngine).EnsureTypeCheckedPackage(ws, "")

	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	return rr.Diagnostics
}

const threeClauseLoop = `package p

func f(n int) {
	for i := 0; i < n; i++ {
		_ = i
	}
}
`

func TestRun_RangeIntHonoursModuleGoVersion(t *testing.T) {
	got := runModernize(t, "go 1.22", map[string]string{"p.go": threeClauseLoop}, modernize.RangeIntAnalyzer)
	if len(got) != 1 {
		t.Fatalf("go 1.22: got %d diagnostics, want 1", len(got))
	}

	got = runModernize(t, "go 1.21", map[string]string{"p.go": threeClauseLoop}, modernize.RangeIntAnalyzer)
	if len(got) != 0 {
		t.Fatalf("go 1.21: got %d diagnostics, want 0", len(got))
	}
}

func TestRun_NewExprFactCrossesPackages(t *testing.T) {
	got := runModernize(t, "go 1.26", map[string]string{
		"zlib/a.go": "package zlib\n\nfunc NewInt(x int) *int {\n\treturn &x\n}\n",
		"app/b.go": "package app\n\nimport \"example.com/m/zlib\"\n\nvar P = zlib.NewInt(3)\n",
	}, modernize.NewExprAnalyzer)

	var calls int

	for _, d := range got {
		if strings.Contains(d.Message, "simplified to new(x)") {
			calls++
		}
	}

	if calls != 1 {
		t.Fatalf("got %d call diagnostics, want 1: %v", calls, got)
	}
}
