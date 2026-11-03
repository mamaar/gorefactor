package srctext_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
)

// parseFuncBody parses src and returns the fileset, source bytes, and the body
// of the first function declaration.
func parseFuncBody(t *testing.T, src string) (*token.FileSet, []byte, *ast.BlockStmt) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Body != nil {
			return fset, []byte(src), fn.Body
		}
	}

	t.Fatal("no function declaration found")
	return nil, nil, nil
}

// firstIfStmt returns the first if statement in the first function's body.
func firstIfStmt(t *testing.T, body *ast.BlockStmt) *ast.IfStmt {
	t.Helper()

	var found *ast.IfStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if ok {
			found = ifStmt
			return false
		}
		return true
	})

	if found == nil {
		t.Fatal("no if statement found")
	}
	return found
}

// TestBlockText_PreservesComments is the regression for the bug this package
// exists to fix: slicing a block from its first statement's Pos to its last
// statement's End drops comments at both edges, so hoisting the block out of
// its braces silently deletes them.
func TestBlockText_PreservesComments(t *testing.T) {
	src := `package p

func f(x bool) {
	if x {
		// leading comment
		a := 1
		_ = a // trailing line comment
		// closing comment
	}
}
`
	fset, content, body := parseFuncBody(t, src)
	ifStmt := firstIfStmt(t, body)

	got := srctext.BlockText(fset, content, ifStmt.Body)

	for _, want := range []string{
		"// leading comment",
		"a := 1",
		"_ = a // trailing line comment",
		"// closing comment",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BlockText dropped %q\ngot:\n%s", want, got)
		}
	}
}

func TestBlockText_TrimsBlankEdgeLinesKeepsInterior(t *testing.T) {
	src := `package p

func f(x bool) {
	if x {

		a := 1

		b := 2

	}
}
`
	fset, content, body := parseFuncBody(t, src)
	ifStmt := firstIfStmt(t, body)

	got := srctext.BlockText(fset, content, ifStmt.Body)

	if strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n") {
		t.Errorf("expected blank edge lines trimmed, got %q", got)
	}
	if !strings.Contains(got, "a := 1\n\n") {
		t.Errorf("expected interior blank line preserved, got %q", got)
	}
}

func TestBlockText_EmptyBlock(t *testing.T) {
	src := `package p

func f(x bool) {
	if x {
	}
}
`
	fset, content, body := parseFuncBody(t, src)
	ifStmt := firstIfStmt(t, body)

	got := srctext.BlockText(fset, content, ifStmt.Body)
	if got != "" {
		t.Errorf("expected empty string for empty block, got %q", got)
	}
}

func TestBlockText_PreservesRawStringLiteral(t *testing.T) {
	src := "package p\n\nfunc f(x bool) {\n\tif x {\n\t\ts := `line one\nline two`\n\t\t_ = s\n\t}\n}\n"

	fset, content, body := parseFuncBody(t, src)
	ifStmt := firstIfStmt(t, body)

	got := srctext.BlockText(fset, content, ifStmt.Body)
	if !strings.Contains(got, "`line one\nline two`") {
		t.Errorf("raw string literal not preserved verbatim, got %q", got)
	}
}

func TestInvertCondition(t *testing.T) {
	tests := []struct {
		name string
		cond string
		want string
	}{
		{"equality", "a == b", "a != b"},
		{"inequality", "a != b", "a == b"},
		{"less", "a < b", "a >= b"},
		{"greater", "a > b", "a <= b"},
		{"less equal", "a <= b", "a > b"},
		{"greater equal", "a >= b", "a < b"},
		// Conjunction/disjunction now distribute via De Morgan instead of
		// falling back to a blanket !(...) wrap: NOT(a==b && c==d) becomes
		// NOT(a==b) || NOT(c==d), and each comparison flips in place.
		{"conjunction", "a == b && c == d", "a != b || c != d"},
		{"disjunction", "a == b || c == d", "a != b && c != d"},
		{"identifier", "ok", "!ok"},
		{"negation drops the not", "!ok", "ok"},
		// A call expression is opaque to PushNeg, so it falls back to a bare
		// `!` wrap - but unlike the old behaviour, no redundant parentheses:
		// `!` already binds tighter than anything a call's own syntax needs
		// protecting from.
		{"call expression", "check(a)", "!check(a)"},
		{"parenthesised", "(a == b)", "a != b"},
		// Regression for bug 4: a call is never wrapped in redundant parens.
		{"selector call expression", "strings.HasPrefix(x, y)", "!strings.HasPrefix(x, y)"},
		// Regression for bug 4: De Morgan distributes through a conjunction of
		// a comparison and a negated selector, rather than blanket-wrapping
		// the whole condition.
		{
			"conjunction of comparison and negated selector",
			"stream.Filter != nil && !stream.Filter.MatchAll",
			"stream.Filter == nil || stream.Filter.MatchAll",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f() {\n\tif " + tt.cond + " {\n\t\tprintln(1)\n\t}\n}\n"
			fset, content, body := parseFuncBody(t, src)
			ifStmt := firstIfStmt(t, body)

			got := srctext.InvertCondition(fset, content, nil, ifStmt.Cond)
			if got != tt.want {
				t.Errorf("InvertCondition(%q) = %q, want %q", tt.cond, got, tt.want)
			}
		})
	}
}

// typeCheck parses and type-checks src, returning the fileset, source bytes,
// the first function's body, and the populated type info.
func typeCheck(t *testing.T, src string) (*token.FileSet, []byte, *ast.BlockStmt, *types.Info) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
	}
	conf := types.Config{Importer: importer.Default()}
	_, err = conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("type check: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Body != nil {
			return fset, []byte(src), fn.Body, info
		}
	}

	t.Fatal("no function declaration found")
	return nil, nil, nil, nil
}

// TestInvertCondition_FloatNaNSafety is the regression for the NaN-unsafety
// bug: flipping an ordering operator changes behavior when either operand is
// a floating-point NaN, so InvertCondition must fall back to a blanket wrap
// for float comparisons when type information says so.
func TestInvertCondition_FloatNaNSafety(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "float less-than wraps instead of flipping",
			src: `package p

func f(x, y float64) bool {
	if x < y {
		return true
	}
	return false
}
`,
			want: "!(x < y)",
		},
		{
			name: "int less-than still flips",
			src: `package p

func f(i, n int) bool {
	if i < n {
		return true
	}
	return false
}
`,
			want: "i >= n",
		},
		{
			name: "float equality is unaffected",
			src: `package p

func f(x, y float64) bool {
	if x == y {
		return true
	}
	return false
}
`,
			want: "x != y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, content, body, info := typeCheck(t, tt.src)
			ifStmt := firstIfStmt(t, body)

			got := srctext.InvertCondition(fset, content, info, ifStmt.Cond)
			if got != tt.want {
				t.Errorf("InvertCondition = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsFloatComparison(t *testing.T) {
	t.Run("float operands", func(t *testing.T) {
		src := `package p

func f(x, y float64) bool {
	return x < y
}
`
		_, _, body, info := typeCheck(t, src)
		binExpr := firstBinaryExpr(t, body)

		if !srctext.IsFloatComparison(info, binExpr) {
			t.Error("expected float operands to report true")
		}
	})

	t.Run("int operands", func(t *testing.T) {
		src := `package p

func f(i, n int) bool {
	return i < n
}
`
		_, _, body, info := typeCheck(t, src)
		binExpr := firstBinaryExpr(t, body)

		if srctext.IsFloatComparison(info, binExpr) {
			t.Error("expected int operands to report false")
		}
	})

	t.Run("mixed float and int operands", func(t *testing.T) {
		src := `package p

func f(x float64, n int) bool {
	return x < float64(n)
}
`
		_, _, body, info := typeCheck(t, src)
		binExpr := firstBinaryExpr(t, body)

		if !srctext.IsFloatComparison(info, binExpr) {
			t.Error("expected mixed float/int operands to report true")
		}
	})

	t.Run("nil info", func(t *testing.T) {
		src := `package p

func f(x, y float64) bool {
	return x < y
}
`
		_, _, body, _ := typeCheck(t, src)
		binExpr := firstBinaryExpr(t, body)

		if srctext.IsFloatComparison(nil, binExpr) {
			t.Error("expected nil info to report false")
		}
	})
}

// firstBinaryExpr returns the first binary expression in body.
func firstBinaryExpr(t *testing.T, body *ast.BlockStmt) *ast.BinaryExpr {
	t.Helper()

	var found *ast.BinaryExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		binExpr, ok := n.(*ast.BinaryExpr)
		if ok {
			found = binExpr
			return false
		}
		return true
	})

	if found == nil {
		t.Fatal("no binary expression found")
	}
	return found
}

func TestIndentation(t *testing.T) {
	content := []byte("package p\n\nfunc f() {\n\t\tif x {\n")
	offset := strings.Index(string(content), "if x")

	got := srctext.Indentation(content, offset)
	if got != "\t\t" {
		t.Errorf("Indentation = %q, want two tabs", got)
	}
}

func TestIndentation_OutOfRange(t *testing.T) {
	content := []byte("package p\n")

	got := srctext.Indentation(content, -1)
	if got != "" {
		t.Errorf("expected empty string for negative offset, got %q", got)
	}
	got = srctext.Indentation(content, len(content)+10)
	if got != "" {
		t.Errorf("expected empty string for out-of-range offset, got %q", got)
	}
}
