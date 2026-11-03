package boolsimplify_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/boolsimplify"
	"github.com/mamaar/gorefactor/pkg/types"
)

// sampleSrc wraps a function body in a package that declares every identifier
// the fixtures use. Nothing here is compiled unless the test asks for type
// information, so the untyped workspace only has to parse.
func sampleSrc(body string) string {
	return `package testpkg

func g(v bool) bool { return v }

func sample(a, b, c, d bool, x, y int) bool {
	` + body + `
}
`
}

func createTestWorkspace(t *testing.T, src string) *types.Workspace {
	t.Helper()
	fileSet := token.NewFileSet()

	astFile, err := parser.ParseFile(fileSet, "testpkg.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse test source: %v", err)
	}

	file := &types.File{
		Path:            "testpkg.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "testpkg",
		Path:  "test/testpkg",
		Files: map[string]*types.File{"testpkg.go": file},
	}
	file.Package = pkg

	return &types.Workspace{
		Packages: map[string]*types.Package{"test/testpkg": pkg},
		FileSet:  fileSet,
	}
}

// createTypedWorkspace is the same, but populates TypesInfo so the analyzer's
// float guard has something to look at. Without it, info.TypeOf returns nil for
// every operand and the guard cannot fire.
func createTypedWorkspace(t *testing.T, src string) *types.Workspace {
	t.Helper()

	ws := createTestWorkspace(t, src)
	pkg := ws.Packages["test/testpkg"]

	info := &gotypes.Info{
		Types: make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:  make(map[*ast.Ident]gotypes.Object),
		Uses:  make(map[*ast.Ident]gotypes.Object),
	}
	conf := gotypes.Config{Importer: importer.Default()}
	typesPkg, err := conf.Check("testpkg", ws.FileSet, []*ast.File{pkg.Files["testpkg.go"].AST}, info)
	if err != nil {
		t.Fatalf("type check: %v", err)
	}
	pkg.TypesInfo = info
	pkg.TypesPkg = typesPkg

	return ws
}

func run(t *testing.T, ws *types.Workspace, opts ...boolsimplify.Option) ([]*boolsimplify.Result, []analysis.Diagnostic) {
	t.Helper()

	rr, err := analyzers.Run(ws, boolsimplify.NewAnalyzer(opts...), "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*boolsimplify.Result)
	if !ok && rr.Result != nil {
		t.Fatalf("unexpected result type %T", rr.Result)
	}
	return results, rr.Diagnostics
}

// runSrc analyses src in an untyped workspace and returns the findings, the
// replacement texts, and the source with every fix applied.
func runSrc(t *testing.T, src string) ([]*boolsimplify.Result, []string, string) {
	t.Helper()

	ws := createTestWorkspace(t, src)
	results, diags := run(t, ws)
	texts, fixed := applyFixes(t, ws.FileSet, src, diags)
	return results, texts, fixed
}

type edit struct {
	start, end int
	text       string
}

// applyFixes splices every suggested fix into src, returning the replacement
// texts in source order alongside the patched source.
func applyFixes(t *testing.T, fset *token.FileSet, src string, diags []analysis.Diagnostic) ([]string, string) {
	t.Helper()

	var edits []edit
	for _, d := range diags {
		for _, fix := range d.SuggestedFixes {
			for _, e := range fix.TextEdits {
				edits = append(edits, edit{
					start: fset.Position(e.Pos).Offset,
					end:   fset.Position(e.End).Offset,
					text:  string(e.NewText),
				})
			}
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })

	for i := 1; i < len(edits); i++ {
		if edits[i].start < edits[i-1].end {
			t.Fatalf("overlapping edits: %+v and %+v", edits[i-1], edits[i])
		}
	}

	texts := make([]string, 0, len(edits))
	for _, e := range edits {
		texts = append(texts, e.text)
	}

	patched := src
	for _, edit := range slices.Backward(edits) {
		patched = patched[:edit.start] + edit.text + patched[edit.end:]
	}
	return texts, patched
}

// returnExpr pulls the expression out of `return <expr>` in func sample.
func returnExpr(t *testing.T, src string) ast.Expr {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "sample" || fn.Body == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if ok && len(ret.Results) == 1 {
				return ret.Results[0]
			}
		}
	}
	t.Fatalf("no return statement found in sample:\n%s", src)
	return nil
}

// fixtures covers every row of the rule table plus the nesting and
// parent-precedence cases. want is the replacement text; "" means the fixture
// must produce no fix at all.
var fixtures = []struct {
	name string
	body string
	want string
}{
	{"de morgan on two negated operands", "return !(!a && !b)", "a || b"},
	{"de morgan on one negated operand", "return !(!a || b)", "a && !b"},
	{"double negation", "return !!a", "a"},
	{"equality flips", "return !(x == y)", "x != y"},
	{"inequality flips", "return !(x != y)", "x == y"},
	{"ordering flips for ints", "return !(x < y)", "x >= y"},
	{"ordering flips for ints, other direction", "return !(x >= y)", "x < y"},
	{"conjunction is left alone", "return !(a && b)", ""},
	{"disjunction is left alone", "return !(a || b)", ""},
	{"plain negation is left alone", "return !a", ""},
	{"no negations at all", "return a && b", ""},
	{"nested negation reports the outermost only", "return !(!(a && b))", "a && b"},
	{"mixed comparisons", "return !(x < 1 && y == 2)", "x >= 1 || y != 2"},
	{"negation nested in an operand", "return !(!a && !(!b && !c))", "a || !b && !c"},
	{"looser result on the left of &&", "return !(!a && !b) && c", "(a || b)"},
	{"looser result on the right of &&", "return c && !(!a && !b)", "(a || b)"},
	{"equal precedence needs no parens", "return !(!a && !b) || c", "a || b"},
	{"comparison result under &&", "return c && !(x == y)", "x != y"},
	{"call argument needs no parens", "return g(!(!a && !b))", "a || b"},
	{"existing parens are enough", "return (!(!a && !b))", "a || b"},
	{"opaque operand blocks the rewrite", "return !(g(!a) && g(!b))", ""},
}

func TestFixtures(t *testing.T) {
	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			results, texts, _ := runSrc(t, sampleSrc(tt.body))

			if tt.want == "" {
				if len(texts) != 0 {
					t.Fatalf("expected no fix, got %q", texts)
				}
				if len(results) != 0 {
					t.Fatalf("expected no findings, got %+v", results[0])
				}
				return
			}

			if len(texts) != 1 {
				t.Fatalf("expected exactly 1 fix, got %d: %q", len(texts), texts)
			}
			if texts[0] != tt.want {
				t.Errorf("fix text = %q, want %q", texts[0], tt.want)
			}
			if len(results) != 1 {
				t.Fatalf("expected exactly 1 finding, got %d", len(results))
			}

			got := results[0]
			if got.Function != "sample" {
				t.Errorf("function = %q, want sample", got.Function)
			}
			if got.After != tt.want {
				t.Errorf("After = %q, want %q", got.After, tt.want)
			}
			if got.NegationsAfter >= got.NegationsBefore {
				t.Errorf("negations did not drop: %d -> %d", got.NegationsBefore, got.NegationsAfter)
			}
			if !strings.HasPrefix(got.Before, "!") {
				t.Errorf("Before = %q, want the verbatim negation", got.Before)
			}
			if got.Line == 0 || got.Column == 0 {
				t.Errorf("missing position: %+v", got)
			}
		})
	}
}

// TestFixedSourceParses guards the property the serializer depends on: it runs
// format.Source over the patched file and, when that fails, writes the file
// unformatted anyway.
func TestFixedSourceParses(t *testing.T) {
	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			_, _, fixed := runSrc(t, sampleSrc(tt.body))
			_, err := parser.ParseFile(token.NewFileSet(), "", fixed, parser.ParseComments)
			if err != nil {
				t.Fatalf("patched source does not parse: %v\n%s", err, fixed)
			}
		})
	}
}

// TestIdempotence re-runs the analyzer over its own output. Every fix strictly
// lowers the negation count, so a second pass over these shapes must find
// nothing left to do.
func TestIdempotence(t *testing.T) {
	for _, tt := range fixtures {
		if tt.want == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			_, texts, fixed := runSrc(t, sampleSrc(tt.body))
			if len(texts) == 0 {
				t.Fatalf("fixture produced no fix")
			}

			_, again, _ := runSrc(t, fixed)
			if len(again) != 0 {
				t.Fatalf("second pass over\n%s\nstill wants %q", fixed, again)
			}
		})
	}
}

// semanticCases are built from boolean identifiers only, so both the original
// and the rewritten expression can be evaluated over the full truth table of
// their free variables. This is the test that catches a precedence bug in the
// splicing: `c && !(!a && !b)` spliced as `c && a || b` parses as
// `(c && a) || b` and disagrees for a=false, b=true, c=false.
var semanticCases = []string{
	"!(!a && !b)",
	"!(!a || b)",
	"!!a",
	"!a",
	"!(a && b)",
	"!(a || b)",
	"!(!(a && b))",
	"c && !(!a && !b)",
	"!(!a && !b) && c",
	"!(!a && !b) || c",
	"c || !(!a && !b)",
	"(!(!a && !b))",
	"!(!a && !(!b && !c))",
	"!((!a && !b) || c)",
	"!((!a || !b) && c)",
	"!(!a && !b) && !(!c && !d)",
	"!(!(a || b) && c)",
	"a && !(!b || !(c && d))",
	"!(!a && b) || !(c && !d)",
	"!(!(!a && !b) && !c)",
	"!(a && !(b || !c))",
	"!!(!a && !b)",
}

func TestSemanticEquivalence(t *testing.T) {
	for _, expr := range semanticCases {
		t.Run(expr, func(t *testing.T) {
			src := sampleSrc("return " + expr)
			_, texts, fixed := runSrc(t, src)
			if len(texts) == 0 {
				return
			}

			before := returnExpr(t, src)
			after := returnExpr(t, fixed)

			vars := freeVars(before)
			for _, v := range freeVars(after) {
				if !contains(vars, v) {
					t.Fatalf("rewrite introduced a new variable %q: %s", v, fixed)
				}
			}
			if len(vars) > 4 {
				t.Fatalf("too many free variables to enumerate: %v", vars)
			}

			for mask := range 1 << len(vars) {
				env := make(map[string]bool, len(vars))
				for i, v := range vars {
					env[v] = mask&(1<<i) != 0
				}
				want := eval(t, before, env)
				got := eval(t, after, env)
				if got != want {
					t.Fatalf("rewrite of %q changed meaning for %v: got %v, want %v\nafter: %s",
						expr, env, got, want, exprString(t, fixed))
				}
			}
		})
	}
}

func exprString(t *testing.T, fixed string) string {
	t.Helper()
	for line := range strings.SplitSeq(fixed, "\n") {
		if strings.Contains(line, "return") && !strings.Contains(line, "func ") {
			return strings.TrimSpace(line)
		}
	}
	return fixed
}

// eval interprets a purely boolean expression. `&&` and `||` short-circuit
// exactly as Go does, which is what makes the comparison meaningful.
func eval(t *testing.T, e ast.Expr, env map[string]bool) bool {
	t.Helper()

	switch x := e.(type) {
	case *ast.ParenExpr:
		return eval(t, x.X, env)
	case *ast.UnaryExpr:
		if x.Op != token.NOT {
			t.Fatalf("unsupported unary operator %s", x.Op)
		}
		return !eval(t, x.X, env)
	case *ast.BinaryExpr:
		switch x.Op {
		case token.LAND:
			return eval(t, x.X, env) && eval(t, x.Y, env)
		case token.LOR:
			return eval(t, x.X, env) || eval(t, x.Y, env)
		}
		t.Fatalf("unsupported binary operator %s", x.Op)
	case *ast.Ident:
		v, ok := env[x.Name]
		if !ok {
			t.Fatalf("undefined variable %q", x.Name)
		}
		return v
	}

	t.Fatalf("unsupported expression %T", e)
	return false
}

func freeVars(e ast.Expr) []string {
	seen := make(map[string]bool)
	var names []string
	ast.Inspect(e, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && !seen[ident.Name] {
			seen[ident.Name] = true
			names = append(names, ident.Name)
		}
		return true
	})
	sort.Strings(names)
	return names
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// TestFloatComparisonIsNotFlipped is the NaN regression: `!(x < y)` is true
// when either operand is NaN, but `x >= y` is false, so the ordering operators
// must survive the rewrite untouched on floating-point operands. The int case
// is the control — same shape, same analyzer, and it does get fixed.
func TestFloatComparisonIsNotFlipped(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		wantFix string
	}{
		{"float operands are left alone", "float64", ""},
		{"int operands are flipped", "int", "x >= y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := `package testpkg

func sample(x, y ` + tt.typ + `) bool {
	return !(x < y)
}
`
			ws := createTypedWorkspace(t, src)
			_, diags := run(t, ws)
			texts, _ := applyFixes(t, ws.FileSet, src, diags)

			if tt.wantFix == "" {
				if len(texts) != 0 {
					t.Fatalf("expected no fix for %s operands, got %q", tt.typ, texts)
				}
				return
			}
			if len(texts) != 1 || texts[0] != tt.wantFix {
				t.Fatalf("fixes = %q, want [%q]", texts, tt.wantFix)
			}
		})
	}
}

// TestFloatComparisonInsideConjunction checks that one unflippable operand
// stops the whole rewrite rather than half of it: `!(x < y && a)` would become
// `!(x < y) || !a`, which is two negations where there was one.
func TestFloatComparisonInsideConjunction(t *testing.T) {
	src := `package testpkg

func sample(x, y float64, a bool) bool {
	return !(x < y && a)
}
`
	ws := createTypedWorkspace(t, src)
	_, diags := run(t, ws)
	texts, _ := applyFixes(t, ws.FileSet, src, diags)
	if len(texts) != 0 {
		t.Fatalf("expected no fix, got %q", texts)
	}
}

// TestNegationInsideOpaqueOperandConverges documents the one shape that needs
// two passes: `!!a` sits inside a call argument that the outer rewrite carries
// over verbatim, and rewriting both at once would mean nesting one text edit
// inside another.
func TestNegationInsideOpaqueOperandConverges(t *testing.T) {
	_, texts, fixed := runSrc(t, sampleSrc("return !(!a && g(!!b))"))
	if len(texts) != 1 || texts[0] != "a || !g(!!b)" {
		t.Fatalf("first pass = %q, want [\"a || !g(!!b)\"]", texts)
	}

	_, texts, fixed = runSrc(t, fixed)
	if len(texts) != 1 || texts[0] != "b" {
		t.Fatalf("second pass = %q, want [\"b\"]", texts)
	}

	_, texts, _ = runSrc(t, fixed)
	if len(texts) != 0 {
		t.Fatalf("third pass = %q, want none", texts)
	}
}

func TestFuncLitAndPackageLevelVar(t *testing.T) {
	src := `package testpkg

var top = !(!alpha && !beta)

var alpha, beta bool

func outer() func() bool {
	return func() bool {
		return !(!alpha && !beta)
	}
}
`
	results, texts, _ := runSrc(t, src)
	if len(texts) != 2 {
		t.Fatalf("expected 2 fixes, got %d: %q", len(texts), texts)
	}
	for _, text := range texts {
		if text != "alpha || beta" {
			t.Errorf("fix text = %q, want %q", text, "alpha || beta")
		}
	}
	if results[0].Function != "" {
		t.Errorf("package-level finding function = %q, want empty", results[0].Function)
	}
	if results[1].Function != "outer" {
		t.Errorf("func literal finding function = %q, want outer", results[1].Function)
	}
}

func TestWithMinReduction(t *testing.T) {
	// `!(!a || b)` drops one negation (2 -> 1); `!(!a && !b)` drops three.
	src := sampleSrc("return !(!a || b)")

	_, texts, _ := runSrc(t, src)
	if len(texts) != 1 {
		t.Fatalf("default config: expected 1 fix, got %q", texts)
	}

	ws := createTestWorkspace(t, src)
	_, diags := run(t, ws, boolsimplify.WithMinReduction(2))
	texts, _ = applyFixes(t, ws.FileSet, src, diags)
	if len(texts) != 0 {
		t.Fatalf("min reduction 2: expected no fix, got %q", texts)
	}

	// A reduction floor below 1 would let the pass rewrite without progress, so
	// it is clamped.
	ws = createTestWorkspace(t, src)
	_, diags = run(t, ws, boolsimplify.WithMinReduction(0))
	texts, _ = applyFixes(t, ws.FileSet, src, diags)
	if len(texts) != 1 {
		t.Fatalf("min reduction 0: expected 1 fix, got %q", texts)
	}
}

// TestCommentsSurvive is the reason this analyzer splices source text instead
// of re-printing the AST: go/printer drops comments from a modified tree.
func TestCommentsSurvive(t *testing.T) {
	src := `package testpkg

func sample(a, b bool) bool {
	// why this matters
	return !(!a && !b) // trailing note
}
`
	_, _, fixed := runSrc(t, src)
	for _, want := range []string{"// why this matters", "// trailing note", "return a || b"} {
		if !strings.Contains(fixed, want) {
			t.Errorf("patched source lost %q:\n%s", want, fixed)
		}
	}
}
