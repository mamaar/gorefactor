package srctext_test

import (
	"go/ast"
	"go/parser"
	"go/token"
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
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
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
		if ifStmt, ok := n.(*ast.IfStmt); ok {
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

	if got := srctext.BlockText(fset, content, ifStmt.Body); got != "" {
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
		{"conjunction", "a == b && c == d", "!(a == b && c == d)"},
		{"disjunction", "a == b || c == d", "!(a == b || c == d)"},
		{"identifier", "ok", "!ok"},
		{"negation drops the not", "!ok", "ok"},
		{"call expression", "check(a)", "!(check(a))"},
		{"parenthesised", "(a == b)", "a != b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f() {\n\tif " + tt.cond + " {\n\t\tprintln(1)\n\t}\n}\n"
			fset, content, body := parseFuncBody(t, src)
			ifStmt := firstIfStmt(t, body)

			if got := srctext.InvertCondition(fset, content, ifStmt.Cond); got != tt.want {
				t.Errorf("InvertCondition(%q) = %q, want %q", tt.cond, got, tt.want)
			}
		})
	}
}

func TestIndentation(t *testing.T) {
	content := []byte("package p\n\nfunc f() {\n\t\tif x {\n")
	offset := strings.Index(string(content), "if x")

	if got := srctext.Indentation(content, offset); got != "\t\t" {
		t.Errorf("Indentation = %q, want two tabs", got)
	}
}

func TestIndentation_OutOfRange(t *testing.T) {
	content := []byte("package p\n")

	if got := srctext.Indentation(content, -1); got != "" {
		t.Errorf("expected empty string for negative offset, got %q", got)
	}
	if got := srctext.Indentation(content, len(content)+10); got != "" {
		t.Errorf("expected empty string for out-of-range offset, got %q", got)
	}
}
