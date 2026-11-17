package mcp

import (
	"go/token"
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
