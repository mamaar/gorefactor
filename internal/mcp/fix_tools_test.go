package mcp

import (
	"context"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

	if len(sel.picks) != 2 || len(sel.skipped) != 1 {
		t.Fatalf("picks=%v skipped=%v, want 2 fixed and 1 skipped", sel.picks, sel.skipped)
	}

	if !strings.Contains(sel.skipped[0].Reason, "overlaps first fix at a.go:") || sel.skipped[0].Analyzer != "second" {
		t.Errorf("skipped reason = %+v", sel.skipped[0])
	}

	if got := len(sel.picks[1].diag.SuggestedFixes[0].TextEdits); got != 2 {
		t.Errorf("second accepted fix has %d edits, want all of its edits kept for checking it alone", got)
	}

	merged := (&fixVerifier{ws: sel.ws}).changes(sel.picks)
	if len(merged) != 3 {
		t.Errorf("merged changes = %+v, want the shared import edit once", merged)
	}
}

func TestVerifiedChanges_RefusesFixThatDoesNotTypeCheckAndKeepsTheRest(t *testing.T) {
	src := "package p\n\nfunc a() int { return 1 }\n\nfunc b() int { return 2 }\n"
	state, ws := loadFixWorkspace(t, map[string]string{"go.mod": "module example.com/vf\n\ngo 1.22\n", "p/p.go": src})

	defer state.Close()

	sel := &fixSelection{
		ws: ws,
		picks: []fixPick{
			{GoFixItem{Message: "bad"}, replaceDiag(t, ws, "p/p.go", "return 1", `return "one"`)},
			{GoFixItem{Message: "good"}, replaceDiag(t, ws, "p/p.go", "return 2", "return 3")},
		},
	}

	changes := sel.verifiedChanges(state.GetEngine().Importer(ws))

	if len(changes) != 1 || changes[0].NewText != "return 3" {
		t.Errorf("changes = %+v, want only the fix that type-checks", changes)
	}

	if fixed := sel.fixedItems(); len(fixed) != 1 || fixed[0].Message != "good" {
		t.Errorf("fixed = %+v, want only the good fix", fixed)
	}

	if len(sel.skipped) != 1 || sel.skipped[0].Message != "bad" || !strings.Contains(sel.skipped[0].Reason, "type errors") {
		t.Errorf("skipped = %+v, want the bad fix with its type error", sel.skipped)
	}
}

func TestVerifiedChanges_SkipsFixesInFilesThatFailTogetherAndKeepsOtherFiles(t *testing.T) {
	state, ws := loadFixWorkspace(t, map[string]string{
		"go.mod": "module example.com/vt\n\ngo 1.22\n",
		"p/a.go": "package p\n\nfunc a() {}\n",
		"p/b.go": "package p\n\nfunc b() {}\n",
		"q/q.go": "package q\n\nfunc c() {}\n",
	})

	defer state.Close()

	sel := &fixSelection{
		ws: ws,
		picks: []fixPick{
			{GoFixItem{Message: "rename a"}, replaceDiag(t, ws, "p/a.go", "func a", "func z")},
			{GoFixItem{Message: "rename b"}, replaceDiag(t, ws, "p/b.go", "func b", "func z")},
			{GoFixItem{Message: "rename c"}, replaceDiag(t, ws, "q/q.go", "func c", "func z")},
		},
	}

	changes := sel.verifiedChanges(state.GetEngine().Importer(ws))

	if len(sel.skipped) != 2 || !strings.Contains(sel.skipped[0].Reason, "redeclared") {
		t.Fatalf("skipped = %+v, want both fixes in package p refused for the redeclaration", sel.skipped)
	}

	if fixed := sel.fixedItems(); len(changes) != 1 || len(fixed) != 1 || fixed[0].Message != "rename c" {
		t.Errorf("changes = %+v, fixed = %+v, want only the fix in q kept", changes, fixed)
	}
}

// replaceDiag returns a diagnostic whose fix replaces the first old in the file ending in rel.
func replaceDiag(t *testing.T, ws *types.Workspace, rel, old, repl string) analysis.Diagnostic {
	t.Helper()

	var file *token.File

	ws.FileSet.Iterate(func(f *token.File) bool {
		if strings.HasSuffix(f.Name(), rel) {
			file = f
		}

		return file == nil
	})

	src, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}

	start := strings.Index(string(src), old)
	edit := analysis.TextEdit{Pos: file.Pos(start), End: file.Pos(start + len(old)), NewText: []byte(repl)}

	return analysis.Diagnostic{Pos: edit.Pos, SuggestedFixes: []analysis.SuggestedFix{{TextEdits: []analysis.TextEdit{edit}}}}
}

func loadFixWorkspace(t *testing.T, files map[string]string) (*MCPServer, *types.Workspace) {
	t.Helper()

	root := t.TempDir()

	for name, content := range files {
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

	_, err := state.LoadWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	ws, err := state.GetWorkspace()
	if err != nil {
		t.Fatal(err)
	}

	state.GetEngine().EnsureTypeCheckedPackage(ws, "")

	return state, ws
}

// applyChanges applies byte-offset changes to src, last first.
func applyChanges(src string, changes []types.Change) string {
	sorted := slices.SortedFunc(slices.Values(changes), func(a, b types.Change) int { return b.Start - a.Start })
	for _, c := range sorted {
		src = src[:c.Start] + c.NewText + src[c.End:]
	}

	return src
}

func TestUnusedImportRemovals_KeepsParenthesesAroundInsertionAndDropsDocComment(t *testing.T) {
	for name, tc := range map[string]struct{ spec, path string }{
		"plain":   {`"sort"`, "sort"},
		"renamed": {`srt "sort"`, "sort"},
		"dot":     {`. "sort"`, "sort"},
	} {
		t.Run(name+" alone in parentheses", func(t *testing.T) {
			src := "package p\n\nimport (\n\t" + tc.spec + "\n)\n"
			_, ws := loadFixWorkspace(t, map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n", "p/p.go": src})
			file := onlyFile(ws)
			at := strings.Index(src, tc.spec)
			insert := types.Change{File: file, Start: at, End: at, NewText: "\"slices\"\n\t"}

			got := unusedImportRemovals(ws, []types.Change{insert}, []string{file + `:4:2: "` + tc.path + `" imported and not used`})

			out := applyChanges(src, append(got, insert))
			if _, err := parser.ParseFile(token.NewFileSet(), "p.go", out, parser.ImportsOnly); err != nil {
				t.Errorf("result does not parse: %v\n%s", err, out)
			}

			if !strings.Contains(out, "import (") || !strings.Contains(out, "\"slices\"") || strings.Contains(out, "sort") {
				t.Errorf("result = %q, want the parenthesized decl holding only slices", out)
			}
		})
	}

	t.Run("doc comment goes with its spec", func(t *testing.T) {
		src := "package p\n\nimport (\n\t\"fmt\"\n\t// sort is for sorting\n\t\"sort\" // trailing\n)\n\nvar _ = fmt.Sprint\n"
		_, ws := loadFixWorkspace(t, map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n", "p/p.go": src})
		file := onlyFile(ws)

		got := unusedImportRemovals(ws, nil, []string{file + `:6:2: "sort" imported and not used`})

		out := applyChanges(src, got)
		if strings.Contains(out, "sorting") || strings.Contains(out, "trailing") || strings.Contains(out, "sort\"") {
			t.Errorf("result = %q, want the spec and both its comments gone", out)
		}
	})
}

func onlyFile(ws *types.Workspace) string {
	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			return f.Path
		}
	}

	return ""
}
