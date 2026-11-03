package switchchain_test

import (
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/switchchain"
	"github.com/mamaar/gorefactor/pkg/types"
)

func createTestWorkspace(t *testing.T, src string) *types.Workspace {
	t.Helper()
	fileSet := token.NewFileSet()

	astFile, err := parser.ParseFile(fileSet, "testpkg.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
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

// createTypeCheckedWorkspace builds a workspace like createTestWorkspace, but
// actually runs go/types over the parsed file so pass.TypesInfo is populated.
// createTestWorkspace hands the analyzer an empty *types.Info, which is fine
// for most rewrites but silently disables any guard that consults it —
// notably planTagged's duplicate-constant check (which needs go/constant
// values to see that a name and a literal denote the same constant) and
// sameType's type-identity comparison for duplicate type-switch cases. Tests
// that exercise those guards must use this helper instead.
func createTypeCheckedWorkspace(t *testing.T, src string) *types.Workspace {
	t.Helper()
	fileSet := token.NewFileSet()

	astFile, err := parser.ParseFile(fileSet, "testpkg.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
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

	conf := gotypes.Config{
		Importer: importer.Default(),
		Error:    func(error) {}, // silently ignore type errors, matching TypeCheckPackage
	}
	info := &gotypes.Info{
		Types:     make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:      make(map[*ast.Ident]gotypes.Object),
		Uses:      make(map[*ast.Ident]gotypes.Object),
		Implicits: make(map[ast.Node]gotypes.Object),
	}
	typesPkg, _ := conf.Check(pkg.Path, fileSet, []*ast.File{astFile}, info)
	pkg.TypesPkg = typesPkg
	pkg.TypesInfo = info

	return &types.Workspace{
		Packages: map[string]*types.Package{"test/testpkg": pkg},
		FileSet:  fileSet,
	}
}

// onlyEdit returns the single suggested text edit across all diagnostics,
// failing the test if there isn't exactly one.
func onlyEdit(t *testing.T, diags []analysis.Diagnostic) analysis.TextEdit {
	t.Helper()
	var found []analysis.TextEdit
	for _, d := range diags {
		for _, fix := range d.SuggestedFixes {
			found = append(found, fix.TextEdits...)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 suggested text edit, got %d", len(found))
	}
	return found[0]
}

// soleFileContent returns the original content of the one file every test
// workspace here holds.
func soleFileContent(ws *types.Workspace) []byte {
	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			return f.OriginalContent
		}
	}
	return nil
}

// assertGofmtStable splices edit into the workspace's original file content
// and checks that gofmt would not touch the result.
//
// A wrapped-condition bug of exactly this kind was already caught once on a
// corpus: wrapIndent's re-indentation of a multi-line condition was off by a
// level, which produced a switch that compiled but that gofmt immediately
// reformatted. Splicing into the whole file (rather than formatting the bare
// fragment in isolation) is what the real pipeline does — the fragment alone
// would format at column zero and this check would pass for the wrong
// reason.
func assertGofmtStable(t *testing.T, ws *types.Workspace, edit analysis.TextEdit) {
	t.Helper()
	content := soleFileContent(ws)
	startOff := ws.FileSet.Position(edit.Pos).Offset
	endOff := ws.FileSet.Position(edit.End).Offset

	spliced := make([]byte, 0, len(content)+len(edit.NewText))
	spliced = append(spliced, content[:startOff]...)
	spliced = append(spliced, edit.NewText...)
	spliced = append(spliced, content[endOff:]...)

	gofmted, err := format.Source(spliced)
	if err != nil {
		t.Fatalf("spliced file does not parse: %v\n%s", err, spliced)
	}
	if string(gofmted) != string(spliced) {
		t.Errorf("rendered switch is not gofmt-stable\ngot:\n%s\ngofmt wants:\n%s", spliced, gofmted)
	}
}

// --- successful conversions ---

// successCase is one chain that must be converted, with the exact rendered
// text pinned. Each case is otherwise independent (different chain shapes,
// different reasons the case matters), but the assertions made against them
// are identical, so they share one table and one runner.
type successCase struct {
	name           string
	src            string
	opts           []switchchain.Option
	wantKind       string
	wantTag        string
	wantCases      []string
	wantBranches   int
	wantHasDefault bool
	wantText       string
}

func TestSuccessfulConversions(t *testing.T) {
	cases := []successCase{
		{
			// The baseline tagged case: an `||` of equalities against one
			// variable becomes `case a, b:`, and a single equality becomes a
			// plain `case k:`.
			name:           "tagged chain with an ORed first link",
			src:            "package testpkg\n\nfunc classify(code int) string {\n\tvar out string\n\tif code == 200 || code == 201 {\n\t\tout = \"ok\"\n\t} else if code == 404 {\n\t\tout = \"not found\"\n\t} else {\n\t\tout = \"error\"\n\t}\n\treturn out\n}\n",
			wantKind:       switchchain.KindTagged,
			wantTag:        "code",
			wantCases:      []string{"200, 201", "404"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch code {\n\tcase 200, 201:\n\t\tout = \"ok\"\n\tcase 404:\n\t\tout = \"not found\"\n\tdefault:\n\t\tout = \"error\"\n\t}",
		},
		{
			// A switch carries a first-link init perfectly well (`switch
			// init; tag {`), unlike the flattener, which refuses any link
			// with one outright.
			name:           "tagged chain with a first-link init",
			src:            "package testpkg\n\nfunc classify() string {\n\tif c := code(); c == 1 {\n\t\treturn \"one\"\n\t} else if c == 2 {\n\t\treturn \"two\"\n\t} else {\n\t\treturn \"other\"\n\t}\n}\n\nfunc code() int { return 0 }\n",
			wantKind:       switchchain.KindTagged,
			wantTag:        "c",
			wantCases:      []string{"1", "2"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch c := code(); c {\n\tcase 1:\n\t\treturn \"one\"\n\tcase 2:\n\t\treturn \"two\"\n\tdefault:\n\t\treturn \"other\"\n\t}",
		},
		{
			// `a == b` says nothing on its own about which side is the
			// dispatch value; matchTagged must try both operands of the
			// first comparison and recognise the tag on whichever side a
			// later link repeats it.
			name:           "tagged chain recognised with the tag on the right of ==",
			src:            "package testpkg\n\nfunc classify(code int) string {\n\tvar out string\n\tif 200 == code {\n\t\tout = \"ok\"\n\t} else if 404 == code {\n\t\tout = \"not found\"\n\t} else {\n\t\tout = \"error\"\n\t}\n\treturn out\n}\n",
			wantKind:       switchchain.KindTagged,
			wantTag:        "code",
			wantCases:      []string{"200", "404"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch code {\n\tcase 200:\n\t\tout = \"ok\"\n\tcase 404:\n\t\tout = \"not found\"\n\tdefault:\n\t\tout = \"error\"\n\t}",
		},
		{
			name:           "tagless chain with the verbatim conditions as cases",
			src:            "package testpkg\n\nfunc grade(score int) string {\n\tvar out string\n\tif score >= 90 {\n\t\tout = \"A\"\n\t} else if score >= 80 {\n\t\tout = \"B\"\n\t} else {\n\t\tout = \"C\"\n\t}\n\treturn out\n}\n",
			wantKind:       switchchain.KindTagless,
			wantTag:        "",
			wantCases:      []string{"score >= 90", "score >= 80"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch {\n\tcase score >= 90:\n\t\tout = \"A\"\n\tcase score >= 80:\n\t\tout = \"B\"\n\tdefault:\n\t\tout = \"C\"\n\t}",
		},
		{
			// This is the regression test for reinstating a purity gate on
			// tagless conditions. A tagless switch hoists nothing: its cases
			// are evaluated top to bottom, exactly as the chain's conditions
			// were, so a call in one is evaluated exactly as often either
			// way. planTagless deliberately has no such check, and this case
			// exists to keep it that way.
			name:           "tagless chain converts even though its conditions call functions",
			src:            "package testpkg\n\nimport \"strings\"\n\nfunc classify(s string) string {\n\tvar out string\n\tif strings.HasPrefix(s, \"a\") {\n\t\tout = \"a\"\n\t} else if len(s) == 0 {\n\t\tout = \"empty\"\n\t} else {\n\t\tout = \"other\"\n\t}\n\treturn out\n}\n",
			wantKind:       switchchain.KindTagless,
			wantTag:        "",
			wantCases:      []string{`strings.HasPrefix(s, "a")`, "len(s) == 0"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch {\n\tcase strings.HasPrefix(s, \"a\"):\n\t\tout = \"a\"\n\tcase len(s) == 0:\n\t\tout = \"empty\"\n\tdefault:\n\t\tout = \"other\"\n\t}",
		},
		{
			name:           "type switch chain with a shared binding name",
			src:            "package testpkg\n\nfunc describe(x any) string {\n\tif v, ok := x.(int); ok {\n\t\treturn itoa(v)\n\t} else if v, ok := x.(string); ok {\n\t\treturn v\n\t} else {\n\t\treturn \"other\"\n\t}\n}\n\nfunc itoa(v int) string { return \"\" }\n",
			wantKind:       switchchain.KindTypeSwitch,
			wantTag:        "x",
			wantCases:      []string{"int", "string"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch v := x.(type) {\n\tcase int:\n\t\treturn itoa(v)\n\tcase string:\n\t\treturn v\n\tdefault:\n\t\treturn \"other\"\n\t}",
		},
		{
			// Go's "declared and not used" rule for a type switch guard is
			// satisfied when any one clause uses the variable, so `_` in
			// every link is legal too — and there is then no variable left
			// to bind at all, giving the operand-only `switch x.(type) {`.
			name:           "type switch chain where every binding is _",
			src:            "package testpkg\n\nfunc describe(x any) string {\n\tif _, ok := x.(int); ok {\n\t\treturn \"int\"\n\t} else if _, ok := x.(string); ok {\n\t\treturn \"string\"\n\t} else {\n\t\treturn \"other\"\n\t}\n}\n",
			wantKind:       switchchain.KindTypeSwitch,
			wantTag:        "x",
			wantCases:      []string{"int", "string"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch x.(type) {\n\tcase int:\n\t\treturn \"int\"\n\tcase string:\n\t\treturn \"string\"\n\tdefault:\n\t\treturn \"other\"\n\t}",
		},
		{
			// Three links and no trailing else: HasDefault is false and the
			// rendered switch carries no default clause. (This chain must
			// not be deferred to the flattener either — see
			// TestDeferralToFlattenerIsSilent for that half of the story.)
			name:           "chain with no final else has no default clause",
			src:            "package testpkg\n\nfunc classify(code int) string {\n\tvar out string\n\tif code == 200 {\n\t\tout = \"ok\"\n\t} else if code == 404 {\n\t\tout = \"not found\"\n\t} else if code == 500 {\n\t\tout = \"error\"\n\t}\n\treturn out\n}\n",
			wantKind:       switchchain.KindTagged,
			wantTag:        "code",
			wantCases:      []string{"200", "404", "500"},
			wantBranches:   3,
			wantHasDefault: false,
			wantText:       "switch code {\n\tcase 200:\n\t\tout = \"ok\"\n\tcase 404:\n\t\tout = \"not found\"\n\tcase 500:\n\t\tout = \"error\"\n\t}",
		},
		{
			// An empty `if` body renders as a bare `case 200:` with nothing
			// under it, not as a case followed by a blank line.
			name:           "empty branch body renders as a bare case label",
			src:            "package testpkg\n\nfunc classify(code int) {\n\tif code == 200 {\n\t} else if code == 404 {\n\t\tlog()\n\t} else {\n\t\tlog()\n\t}\n}\n\nfunc log() {}\n",
			wantKind:       switchchain.KindTagged,
			wantTag:        "code",
			wantCases:      []string{"200", "404"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch code {\n\tcase 200:\n\tcase 404:\n\t\tlog()\n\tdefault:\n\t\tlog()\n\t}",
		},
		{
			// The rewrite lifts source text verbatim rather than
			// re-printing the AST, so a comment inside a branch body must
			// survive the conversion unchanged.
			name:           "comment inside a branch body survives verbatim",
			src:            "package testpkg\n\nfunc classify(code int) string {\n\tvar out string\n\tif code == 200 {\n\t\t// success\n\t\tout = \"ok\"\n\t} else if code == 404 {\n\t\tout = \"not found\"\n\t} else {\n\t\tout = \"error\"\n\t}\n\treturn out\n}\n",
			wantKind:       switchchain.KindTagged,
			wantTag:        "code",
			wantCases:      []string{"200", "404"},
			wantBranches:   3,
			wantHasDefault: true,
			wantText:       "switch code {\n\tcase 200:\n\t\t// success\n\t\tout = \"ok\"\n\tcase 404:\n\t\tout = \"not found\"\n\tdefault:\n\t\tout = \"error\"\n\t}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := createTestWorkspace(t, tc.src)
			a := switchchain.NewAnalyzer(tc.opts...)
			rr, err := analyzers.Run(ws, a, "")
			if err != nil {
				t.Fatal(err)
			}

			results, ok := rr.Result.([]*switchchain.Result)
			if !ok {
				t.Fatalf("expected []*switchchain.Result, got %T", rr.Result)
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			r := results[0]

			if !r.Fixable {
				t.Fatalf("expected a fixable chain, reason: %s", r.Reason)
			}
			if r.Reason != "" {
				t.Errorf("a fixable result should carry no reason, got %q", r.Reason)
			}
			if r.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", r.Kind, tc.wantKind)
			}
			if r.Tag != tc.wantTag {
				t.Errorf("Tag = %q, want %q", r.Tag, tc.wantTag)
			}
			if r.Branches != tc.wantBranches {
				t.Errorf("Branches = %d, want %d", r.Branches, tc.wantBranches)
			}
			if r.HasDefault != tc.wantHasDefault {
				t.Errorf("HasDefault = %v, want %v", r.HasDefault, tc.wantHasDefault)
			}
			if len(r.Cases) != len(tc.wantCases) {
				t.Fatalf("Cases = %v, want %v", r.Cases, tc.wantCases)
			}
			for i, want := range tc.wantCases {
				if r.Cases[i] != want {
					t.Errorf("Cases[%d] = %q, want %q", i, r.Cases[i], want)
				}
			}

			edit := onlyEdit(t, rr.Diagnostics)
			got := string(edit.NewText)
			if got != tc.wantText {
				t.Errorf("rendered text mismatch\ngot:\n%s\nwant:\n%s", got, tc.wantText)
			}
			assertGofmtStable(t, ws, edit)
		})
	}
}

// --- deferral to the flattener ---

// TestDeferralToFlattenerIsSilent covers the single most important boundary
// in this package: a chain where every link body terminates, the final else
// terminates too, and no link has an Init is left entirely alone, because
// fix_deep_if_else_chains (package deepifelse) already rewrites that exact
// shape by eliding the else after every terminating branch. Reporting it
// here too would put two fixers on the same code, each producing a
// whole-chain edit.
func TestDeferralToFlattenerIsSilent(t *testing.T) {
	src := `package testpkg

func classify(code int) string {
	if code == 200 {
		return "ok"
	} else if code == 404 {
		return "not found"
	} else {
		return "error"
	}
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 0 {
		t.Fatalf("expected the chain to be silently deferred to the flattener, got %d results", len(results))
	}
	if len(rr.Diagnostics) != 0 {
		t.Fatalf("expected no diagnostics for a deferred chain, got %d", len(rr.Diagnostics))
	}
}

// TestDeferralBypassedByFirstLinkInit is the converse of
// TestDeferralToFlattenerIsSilent: the same three terminating branches, but
// now the first link carries an init. The flattener refuses any link with
// one outright ("run fix_if_init_assignments first"), so termination is not
// consulted at all once a link has an Init — this chain is switchchain's
// regardless of what its branches do.
func TestDeferralBypassedByFirstLinkInit(t *testing.T) {
	src := `package testpkg

func classify() string {
	if c := code(); c == 200 {
		return "ok"
	} else if c == 404 {
		return "not found"
	} else {
		return "error"
	}
}

func code() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 1 {
		t.Fatalf("expected the chain to be reported once the first link has an init, got %d results", len(results))
	}
	if !results[0].Fixable {
		t.Fatalf("expected the chain to be fixable, reason: %s", results[0].Reason)
	}

	edit := onlyEdit(t, rr.Diagnostics)
	want := "switch c := code(); c {\n\tcase 200:\n\t\treturn \"ok\"\n\tcase 404:\n\t\treturn \"not found\"\n\tdefault:\n\t\treturn \"error\"\n\t}"
	got := string(edit.NewText)
	if got != want {
		t.Errorf("rendered text mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
	assertGofmtStable(t, ws, edit)
}

// --- refusals ---

// refusalCase is one chain the analyzer classifies but refuses to rewrite.
// The set is homogeneous — construct a workspace, run the analyzer, assert
// Fixable is false and Reason matches exactly, assert no SuggestedFix was
// attached — so it is one table.
type refusalCase struct {
	name        string
	src         string
	typechecked bool
	wantReason  string
}

func TestRefusals(t *testing.T) {
	cases := []refusalCase{
		{
			// A bare break in a branch currently leaves the enclosing for.
			// Wrapping the branches in a new switch would make that switch
			// intercept it instead, silently changing where control goes.
			// The final else is deliberately non-terminating (an
			// assignment) so the chain is not deferred to the flattener
			// before the hazard even gets a chance to fire.
			name: "a branch breaks out of an enclosing loop",
			src: `package testpkg

func classify(items []int) string {
	var out string
	for _, code := range items {
		if code == 200 {
			break
		} else if code == 404 {
			return "not found"
		} else {
			out = "error"
		}
	}
	return out
}
`,
			wantReason: "a branch breaks out of an enclosing loop or switch, which the new switch would intercept",
		},
		{
			name: "the else branch breaks out of an enclosing loop",
			src: `package testpkg

func classify(items []int) string {
	var out string
	for _, code := range items {
		if code == 200 {
			out = "ok"
		} else if code == 404 {
			out = "not found"
		} else {
			break
		}
	}
	return out
}
`,
			wantReason: "the else branch breaks out of an enclosing loop or switch, which the new switch would intercept",
		},
		{
			// Rendering runs every branch body through srctext.Normalize,
			// which strips the indentation the lines share. A raw string's
			// interior indentation is part of its value, not layout, so
			// that would corrupt it.
			name: "a multi-line raw string literal anywhere in the chain",
			src: "package testpkg\n\nfunc render(code int) string {\n" +
				"\tvar out string\n" +
				"\tif code == 200 {\n\t\tout = \"ok\"\n\t} else if code == 404 {\n\t\tout = \"not found\"\n\t} else {\n" +
				"\t\tout = `multi\nline`\n" +
				"\t}\n\treturn out\n}\n",
			wantReason: "the chain contains a multi-line raw string literal, whose indentation is part of its value",
		},
		{
			// The rewrite keeps branch bodies verbatim and rebuilds
			// everything around them, so a comment between the branches —
			// here, on the `else if` line itself — has nowhere to go and
			// would be silently deleted.
			name: "a comment sits between the chain's branches",
			src: `package testpkg

func classify(c int) string {
	var out string
	if c == 1 {
		out = "one"
	} else /* why */ if c == 2 {
		out = "two"
	} else {
		out = "other"
	}
	return out
}
`,
			wantReason: "a comment sits between the chain's branches, where a switch has nowhere to keep it",
		},
		{
			// `switch init; x {` carries exactly one init statement, the
			// chain's first. A later link's init has nowhere to go once the
			// chain becomes a set of sibling clauses.
			name: "a later link declares variables in its if header",
			src: `package testpkg

func classify() string {
	var out string
	if code() == 200 {
		out = "ok"
	} else if c := code(); c == 404 {
		out = "not found"
	} else {
		out = "error"
	}
	return out
}

func code() int { return 0 }
`,
			wantReason: "a later link declares variables in its if header; run fix_if_init_assignments first",
		},
		{
			// The tag is hoisted: the chain read it once per comparison it
			// tried, a switch reads it once. A call can hide inside a path
			// expression's index, as it does here.
			name: "the switch tag calls a function",
			src: `package testpkg

func classify(m map[int]int) string {
	var out string
	if m[f()] == 1 {
		out = "one"
	} else if m[f()] == 2 {
		out = "two"
	} else {
		out = "other"
	}
	return out
}

func f() int { return 0 }
`,
			wantReason: "the switch tag calls a function or receives from a channel, so a switch would evaluate it once where the chain evaluated it once per branch",
		},
		{
			// go/types resolves the value behind a name, so a literal
			// `"a"` and a constant that also denotes "a" are recognised as
			// the same case value even though the source text differs.
			// This needs a type-checked workspace: createTestWorkspace's
			// empty types.Info would let constValue return nil for both
			// operands and this guard would silently never fire.
			name:        "two branches test the tag against the same value, one of them through a const alias",
			typechecked: true,
			src: `package testpkg

const b = "a"

func classify(s string) string {
	var out string
	if s == "a" {
		out = "one"
	} else if s == b {
		out = "two"
	} else {
		out = "other"
	}
	return out
}
`,
			wantReason: "two branches test the tag against the same value b, which a switch cannot repeat; remove the dead branch first",
		},
		{
			name: "the branches assert on different expressions",
			src: `package testpkg

func describe(x, y any) string {
	if v, ok := x.(int); ok {
		return itoa2(v)
	} else if v, ok := y.(string); ok {
		return v
	} else {
		return "other"
	}
}

func itoa2(v int) string { return "" }
`,
			wantReason: "the branches assert on different expressions, which a single type switch cannot do",
		},
		{
			name: "two branches assert the same type",
			src: `package testpkg

func describe(x any) string {
	if v, ok := x.(int); ok {
		return itoa2(v)
	} else if v, ok := x.(int); ok {
		return itoa2(v)
	} else {
		return "other"
	}
}

func itoa2(v int) string { return "" }
`,
			wantReason: "two branches assert the same type int, which a type switch cannot repeat; remove the dead branch first",
		},
		{
			name: "the branches bind the asserted value to different names",
			src: `package testpkg

func describe(x any) string {
	if v, ok := x.(int); ok {
		return itoa2(v)
	} else if w, ok := x.(string); ok {
		return w
	} else {
		return "other"
	}
}

func itoa2(v int) string { return "" }
`,
			wantReason: `the branches bind the asserted value to different names ("v" and "w"), which a single type switch guard cannot do`,
		},
		{
			// `ok` exists only because the assertion was written as a
			// statement. A type switch has no such variable, so a body that
			// reads one cannot be carried over.
			name: "a branch reads the assertion's ok variable",
			src: `package testpkg

func describe(x any) string {
	if v, ok := x.(int); ok {
		return itoa2(v)
	} else if v, ok := x.(string); ok {
		_ = ok
		return v
	} else {
		return "other"
	}
}

func itoa2(v int) string { return "" }
`,
			wantReason: "a branch reads the assertion's ok variable, which a type switch does not bind",
		},
		{
			name: "the else branch reads the assertion's ok variable",
			src: `package testpkg

func describe(x any) any {
	if v, ok := x.(int); ok {
		return v
	} else if v, ok := x.(string); ok {
		return v
	} else {
		return ok
	}
}
`,
			wantReason: "the else branch reads the assertion's ok variable, which a type switch does not bind",
		},
		{
			// The guard variable is declared in the clause's own implicit
			// block, the same one the body statements live in. A body that
			// shadows it with its own declaration compiles today (`v :=
			// len(v)`) and would stop compiling as a clause, since the
			// clause already binds v.
			name: "a branch redeclares the type switch binding in its own body",
			src: `package testpkg

func describe(x any) any {
	if v, ok := x.(int); ok {
		return v
	} else if v, ok := x.(string); ok {
		v := len(v)
		return v
	} else {
		return 0
	}
}
`,
			wantReason: `a branch redeclares "v", which the type switch guard already binds in that clause`,
		},
		{
			// A link that binds "_" declares no v of its own for that
			// clause, so a read of that name inside it comes from an
			// enclosing scope today. In a type switch every clause binds
			// the name, so the read would silently change meaning.
			name: "a branch with no binding of its own reads an outer name the guard would rebind",
			src: `package testpkg

func describe(v2 int, x any) any {
	if v2, ok := x.(int); ok {
		return v2
	} else if _, ok := x.(string); ok {
		return v2
	} else {
		return 0
	}
}
`,
			wantReason: `a branch reads an outer "v2", which the type switch would rebind in every clause`,
		},
		{
			name: "the else branch reads an outer name the guard would shadow",
			src: `package testpkg

func describe(x any) any {
	if v, ok := x.(int); ok {
		return v
	} else if v, ok := x.(string); ok {
		return v
	} else {
		return v
	}
}
`,
			wantReason: `the else branch reads an outer "v", which the new default clause would shadow`,
		},
		{
			// The asserted expression is the one thing a type switch really
			// hoists: the chain asserted on it once per link, the guard
			// asserts on it once. Unlike the tagged form's tag there is no
			// pure-path requirement standing in the way first, since the
			// operand can be any expression at all — so a bare call is
			// caught here directly.
			name: "the asserted expression calls a function",
			src: `package testpkg

func describe(x any) any {
	if v, ok := f(x).(int); ok {
		return v
	} else if v, ok := f(x).(string); ok {
		return v
	} else {
		return 0
	}
}

func f(x any) any { return x }
`,
			wantReason: "the asserted expression calls a function or receives from a channel, so a type switch would not evaluate it as often as the chain did",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ws *types.Workspace
			if tc.typechecked {
				ws = createTypeCheckedWorkspace(t, tc.src)
			} else {
				ws = createTestWorkspace(t, tc.src)
			}

			rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
			if err != nil {
				t.Fatal(err)
			}

			results, _ := rr.Result.([]*switchchain.Result)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			r := results[0]

			if r.Fixable {
				t.Error("expected the chain to be refused")
			}
			if r.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", r.Reason, tc.wantReason)
			}

			for _, d := range rr.Diagnostics {
				if len(d.SuggestedFixes) != 0 {
					t.Error("a refused finding must not carry a suggested fix")
				}
			}
		})
	}
}

// --- options ---

// TestMinBranchesDefaultSilencesAPlainIfElse covers the low end of the
// threshold: a plain if/else is two branches, one short of gocritic's
// ifElseChain minimum, and is silent under the default configuration. The
// branch bodies are assignments rather than returns so the result is silent
// purely because of the branch count, not because it was deferred to the
// flattener (see TestDeferralToFlattenerIsSilent for that separate reason).
func TestMinBranchesDefaultSilencesAPlainIfElse(t *testing.T) {
	src := `package testpkg

func classify(code int) string {
	var out string
	if code == 200 {
		out = "ok"
	} else {
		out = "error"
	}
	return out
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 0 {
		t.Fatalf("expected a plain if/else to be silent under the default threshold, got %d results", len(results))
	}
}

// TestWithMinBranchesLowersTheThreshold reruns the same two-branch chain
// with WithMinBranches(2) and expects it reported.
func TestWithMinBranchesLowersTheThreshold(t *testing.T) {
	src := `package testpkg

func classify(code int) string {
	var out string
	if code == 200 {
		out = "ok"
	} else {
		out = "error"
	}
	return out
}
`
	ws := createTestWorkspace(t, src)
	a := switchchain.NewAnalyzer(switchchain.WithMinBranches(2))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 1 {
		t.Fatalf("expected the 2-branch chain to be reported with WithMinBranches(2), got %d results", len(results))
	}
	if !results[0].Fixable {
		t.Fatalf("expected the chain to be fixable, reason: %s", results[0].Reason)
	}
	if results[0].Branches != 2 {
		t.Errorf("Branches = %d, want 2", results[0].Branches)
	}
}

// TestWithKindsFiltersByForm covers WithKinds: restricting to KindTagged
// reports a tagged chain and stays silent on a tagless one in the same file.
func TestWithKindsFiltersByForm(t *testing.T) {
	src := `package testpkg

func tagged(code int) string {
	var out string
	if code == 200 {
		out = "ok"
	} else if code == 404 {
		out = "not found"
	} else {
		out = "error"
	}
	return out
}

func tagless(score int) string {
	var out string
	if score > 90 {
		out = "A"
	} else if score > 80 {
		out = "B"
	} else {
		out = "C"
	}
	return out
}
`
	ws := createTestWorkspace(t, src)
	a := switchchain.NewAnalyzer(switchchain.WithKinds(switchchain.KindTagged))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 1 {
		t.Fatalf("expected only the tagged chain to be reported, got %d results", len(results))
	}
	if results[0].Kind != switchchain.KindTagged {
		t.Errorf("Kind = %q, want %q", results[0].Kind, switchchain.KindTagged)
	}
	if results[0].Function != "tagged" {
		t.Errorf("Function = %q, want %q", results[0].Function, "tagged")
	}
}

// --- structural ---

// TestNestedChainProducesOneNonOverlappingEdit covers a chain inside another
// chain's branch body. The outer chain's rewrite carries the inner one's
// source text over verbatim, so the inner chain is left for a later run to
// find; reporting it here too would produce two edits, one nested inside the
// other, which Serializer.validateChangePositions rejects outright.
func TestNestedChainProducesOneNonOverlappingEdit(t *testing.T) {
	src := `package testpkg

func classify(a, b int) string {
	var out string
	if a == 1 {
		if b == 1 {
			out = "a1b1"
		} else if b == 2 {
			out = "a1b2"
		} else {
			out = "a1other"
		}
	} else if a == 2 {
		out = "a2"
	} else {
		out = "aother"
	}
	return out
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result (the outer chain only), got %d", len(results))
	}

	var edits []analysis.TextEdit
	for _, d := range rr.Diagnostics {
		for _, fix := range d.SuggestedFixes {
			edits = append(edits, fix.TextEdits...)
		}
	}
	if len(edits) != 1 {
		t.Fatalf("expected exactly 1 text edit, got %d", len(edits))
	}

	edit := edits[0]
	want := "switch a {\n\tcase 1:\n\t\tif b == 1 {\n\t\t\tout = \"a1b1\"\n\t\t} else if b == 2 {\n\t\t\tout = \"a1b2\"\n\t\t} else {\n\t\t\tout = \"a1other\"\n\t\t}\n\tcase 2:\n\t\tout = \"a2\"\n\tdefault:\n\t\tout = \"aother\"\n\t}"
	got := string(edit.NewText)
	if got != want {
		t.Errorf("rendered text mismatch (inner chain should survive verbatim)\ngot:\n%s\nwant:\n%s", got, want)
	}
	assertGofmtStable(t, ws, edit)
}

// TestChainInsideFunctionLiteralIsFound covers walkFile's second traversal:
// a function literal opens its own function scope, so its body is a root in
// its own right and needs its own visit via ast.Inspect rather than the
// structural walk (which never descends into expressions).
func TestChainInsideFunctionLiteralIsFound(t *testing.T) {
	src := `package testpkg

func makeFn() func(int) string {
	return func(code int) string {
		var out string
		if code == 200 {
			out = "ok"
		} else if code == 404 {
			out = "not found"
		} else {
			out = "error"
		}
		return out
	}
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 1 {
		t.Fatalf("expected the chain inside the function literal to be found, got %d results", len(results))
	}
	if !strings.HasSuffix(results[0].Function, ".func") {
		t.Errorf("Function = %q, want it to end in \".func\"", results[0].Function)
	}
	if !results[0].Fixable {
		t.Fatalf("expected the chain to be fixable, reason: %s", results[0].Reason)
	}
}

// TestFixableResultsAndSuggestedFixesStayInLockstep is the regression guard
// for the invariant the MCP layer depends on: makeRun reports one diagnostic
// and appends one Result per chain, in the same order, and attaches a
// SuggestedFix only when the Result is Fixable. The MCP fixer pairs
// fixableResults[i] with changes[i] by index and refuses to apply anything
// if the two counts disagree, so a diagnostic reported without its Result,
// appended out of order, or carrying a fix for an unfixable finding would
// break the pairing silently. This source carries one fixable tagged chain
// and one chain refused for breaking out of its enclosing loop, so both
// sides of the pairing are exercised in the same run.
func TestFixableResultsAndSuggestedFixesStayInLockstep(t *testing.T) {
	src := `package testpkg

func mixed(a int, items []int) (string, string) {
	var out1 string
	if a == 1 {
		out1 = "one"
	} else if a == 2 {
		out1 = "two"
	} else {
		out1 = "other"
	}

	var out2 string
	for _, i := range items {
		if i == 1 {
			break
		} else if i == 2 {
			out2 = "two"
		} else {
			out2 = "other"
		}
	}
	return out1, out2
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*switchchain.Result)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if len(rr.Diagnostics) != len(results) {
		t.Fatalf("expected 1 diagnostic per result, got %d diagnostics for %d results", len(rr.Diagnostics), len(results))
	}

	fixableCount := 0
	fixCount := 0
	for i, r := range results {
		hasFix := len(rr.Diagnostics[i].SuggestedFixes) != 0
		if hasFix != r.Fixable {
			t.Errorf("result %d: Fixable = %v but diagnostic carries a fix = %v (out of lockstep)", i, r.Fixable, hasFix)
		}
		if r.Fixable {
			fixableCount++
		}
		if hasFix {
			fixCount++
		}
	}
	if fixableCount != fixCount {
		t.Errorf("fixable results = %d, diagnostics with a fix = %d, want equal", fixableCount, fixCount)
	}
	if fixableCount != 1 {
		t.Fatalf("expected exactly 1 fixable result in this fixture, got %d", fixableCount)
	}
}
