package mcp

import (
	"context"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestFixSelection_OverlapIsSkippedAndIdenticalEditsDeduplicated(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("/ws/a.go", -1, 100)
	f.SetLines([]int{0, 50})

	diag := func(msg string, edits ...[3]any) analysis.Diagnostic {
		var te []analysis.TextEdit
		for _, e := range edits {
			te = append(te, analysis.TextEdit{Pos: f.Pos(e[0].(int)), End: f.Pos(e[1].(int)), NewText: []byte(e[2].(string))})
		}

		return analysis.Diagnostic{Pos: f.Pos(10), Message: msg, SuggestedFixes: []analysis.SuggestedFix{{Message: msg, TextEdits: te}}}
	}

	sel := &fixSelection{ws: &types.Workspace{FileSet: fset, RootPath: "/ws"}, generated: map[string]bool{}}
	sel.add("first", []analysis.Diagnostic{diag("one", [3]any{0, 0, "import x"}, [3]any{10, 20, "A"})})
	sel.add("second", []analysis.Diagnostic{
		diag("two", [3]any{0, 0, "import x"}, [3]any{30, 40, "B"}), // shares the import edit, disjoint body
		diag("three", [3]any{15, 25, "C"}),                         // overlaps "one"
	})

	if len(sel.fixed) != 2 || len(sel.skipped) != 1 {
		t.Fatalf("fixed=%v skipped=%v, want 2 fixed and 1 skipped", sel.fixed, sel.skipped)
	}

	if !strings.Contains(sel.skipped[0].Reason, "overlaps first fix at a.go:") || sel.skipped[0].Analyzer != "second" {
		t.Errorf("skipped reason = %+v", sel.skipped[0])
	}

	if got := len(sel.diags[1].SuggestedFixes[0].TextEdits); got != 1 {
		t.Errorf("second accepted fix has %d edits, want the duplicate import edit dropped", got)
	}
}

func TestVerifiedChanges_RefusesFixThatDoesNotTypeCheckAndKeepsTheRest(t *testing.T) {
	root := t.TempDir()
	src := "package p\n\nfunc a() int { return 1 }\n\nfunc b() int { return 2 }\n"

	for name, content := range map[string]string{"go.mod": "module example.com/vf\n\ngo 1.22\n", "p/p.go": src} {
		path := filepath.Join(root, name)

		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(path, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	state := NewMCPServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer state.Close()

	_, err := state.LoadWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	ws, err := state.GetWorkspace()
	if err != nil {
		t.Fatal(err)
	}

	state.GetEngine().EnsureTypeCheckedPackage(ws, "")

	var file *token.File

	ws.FileSet.Iterate(func(f *token.File) bool {
		if strings.HasSuffix(f.Name(), "p/p.go") {
			file = f
		}

		return file == nil
	})

	replace := func(old, repl string) analysis.Diagnostic {
		start := strings.Index(src, old)
		edit := analysis.TextEdit{Pos: file.Pos(start), End: file.Pos(start + len(old)), NewText: []byte(repl)}

		return analysis.Diagnostic{Pos: edit.Pos, SuggestedFixes: []analysis.SuggestedFix{{TextEdits: []analysis.TextEdit{edit}}}}
	}

	sel := &fixSelection{
		ws:    ws,
		diags: []analysis.Diagnostic{replace("return 1", `return "one"`), replace("return 2", "return 3")},
		fixed: []GoFixItem{{Message: "bad"}, {Message: "good"}},
	}

	changes, err := sel.verifiedChanges(state.GetEngine().Importer(ws))
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 1 || changes[0].NewText != "return 3" {
		t.Errorf("changes = %+v, want only the fix that type-checks", changes)
	}

	if len(sel.fixed) != 1 || sel.fixed[0].Message != "good" {
		t.Errorf("fixed = %+v, want only the good fix", sel.fixed)
	}

	if len(sel.skipped) != 1 || sel.skipped[0].Message != "bad" || !strings.Contains(sel.skipped[0].Reason, "type errors") {
		t.Errorf("skipped = %+v, want the bad fix with its type error", sel.skipped)
	}
}
