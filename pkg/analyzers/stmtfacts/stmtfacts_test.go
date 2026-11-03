package stmtfacts_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
)

// parseBody parses a file holding a single function named f and returns the
// last statement of its body, which is the one every Terminates case is about.
func parseBody(t *testing.T, src string) (ast.Stmt, *types.Info) {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{
		Importer: importer.Default(),
		// Test sources are deliberately minimal and may not be fully valid
		// (unreachable code, unused results); type errors must not stop the
		// Uses map from being populated for the parts that do resolve.
		Error: func(error) {},
	}
	_, _ = conf.Check("testpkg", fset, []*ast.File{file}, info)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "f" || fn.Body == nil {
			continue
		}

		if len(fn.Body.List) == 0 {
			t.Fatal("function f has an empty body")
		}

		return fn.Body.List[len(fn.Body.List)-1], info
	}

	t.Fatal("no function named f")

	return nil, nil
}

func TestTerminates(t *testing.T) {
	tests := []struct {
		name string
		want bool
		src  string
	}{
		{
			name: "return",
			want: true,
			src:  "package testpkg\nfunc f() int {\nreturn 1\n}\n",
		},
		{
			name: "panic call",
			want: true,
			src:  "package testpkg\nfunc f() {\npanic(\"boom\")\n}\n",
		},
		{
			// A local declaration shadows the builtin, so the call returns
			// normally and control carries on past it.
			name: "shadowed panic is not terminating",
			want: false,
			src: `package testpkg

func f() {
	panic := func(string) {}
	panic("boom")
}
`,
		},
		{
			name: "goto",
			want: true,
			src: `package testpkg

func f() {
L:
	for {
		break L
	}
	goto L
}
`,
		},
		{
			// The case that unblocks the real chains: every arm returns.
			name: "if-else where both arms return",
			want: true,
			src: `package testpkg

func f(a bool) int {
	if a {
		return 1
	} else {
		return 2
	}
}
`,
		},
		{
			name: "else-if chain where every arm returns",
			want: true,
			src: `package testpkg

func f(a, b bool) int {
	if a {
		return 1
	} else if b {
		return 2
	} else {
		return 3
	}
}
`,
		},
		{
			name: "if without else",
			want: false,
			src: `package testpkg

func f(a bool) int {
	if a {
		return 1
	}
}
`,
		},
		{
			name: "if-else where one arm falls through",
			want: false,
			src: `package testpkg

func f(a bool) int {
	if a {
		return 1
	} else {
		_ = a
	}
}
`,
		},
		{
			name: "block ending in return",
			want: true,
			src: `package testpkg

func f() int {
	{
		return 1
	}
}
`,
		},
		{
			name: "for without condition",
			want: true,
			src: `package testpkg

func f() {
	for {
		_ = 1
	}
}
`,
		},
		{
			name: "for without condition but with a break",
			want: false,
			src: `package testpkg

func f(a bool) {
	for {
		if a {
			break
		}
	}
}
`,
		},
		{
			// The break belongs to the inner switch, not to the loop, so the
			// loop still never finishes.
			name: "for whose break belongs to a nested switch",
			want: true,
			src: `package testpkg

func f(a int) {
	for {
		switch a {
		case 1:
			break
		}
	}
}
`,
		},
		{
			name: "for with a labelled break aimed at it",
			want: false,
			src: `package testpkg

func f(a int) {
Loop:
	for {
		switch a {
		case 1:
			break Loop
		}
	}
}
`,
		},
		{
			name: "for with a condition",
			want: false,
			src: `package testpkg

func f(a bool) {
	for a {
		_ = a
	}
}
`,
		},
		{
			name: "range loop",
			want: false,
			src: `package testpkg

func f(xs []int) {
	for range xs {
		_ = 1
	}
}
`,
		},
		{
			name: "switch with default where every clause returns",
			want: true,
			src: `package testpkg

func f(a int) int {
	switch a {
	case 1:
		return 1
	default:
		return 2
	}
}
`,
		},
		{
			name: "switch without default",
			want: false,
			src: `package testpkg

func f(a int) int {
	switch a {
	case 1:
		return 1
	case 2:
		return 2
	}
}
`,
		},
		{
			// The break lands just past the switch, so control continues even
			// though the clause's last statement is a return.
			name: "switch with a break in a non-final position",
			want: false,
			src: `package testpkg

func f(a int, b bool) int {
	switch a {
	case 1:
		if b {
			break
		}
		return 1
	default:
		return 2
	}
}
`,
		},
		{
			name: "switch clause ending in fallthrough",
			want: true,
			src: `package testpkg

func f(a int) int {
	switch a {
	case 1:
		fallthrough
	default:
		return 2
	}
}
`,
		},
		{
			name: "type switch with default where every clause returns",
			want: true,
			src: `package testpkg

func f(a any) int {
	switch a.(type) {
	case int:
		return 1
	default:
		return 2
	}
}
`,
		},
		{
			// Unlike a switch, a select has no "nothing matched" path out.
			name: "select without default where every clause returns",
			want: true,
			src: `package testpkg

func f(ch chan int) int {
	select {
	case <-ch:
		return 1
	}
}
`,
		},
		{
			name: "select with a break",
			want: false,
			src: `package testpkg

func f(ch chan int) int {
	select {
	case <-ch:
		break
	}
}
`,
		},
		{
			name: "labelled terminating statement",
			want: true,
			src: `package testpkg

func f() {
L:
	for {
		_ = 1
	}
}
`,
		},
		{
			name: "plain assignment",
			want: false,
			src: `package testpkg

func f(a int) {
	a = 1
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, info := parseBody(t, tt.src)

			got := stmtfacts.Terminates(info, stmt)
			if got != tt.want {
				t.Errorf("Terminates() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The deliberate deviation from the Go spec: a bare break or continue does
// leave the block it is written in, which is exactly what callers ask about
// when they want to know whether an else can be dropped. The spec excludes
// both because it is answering a different question.
func TestTerminatesBranchStatementsLeaveTheirBlock(t *testing.T) {
	src := `package testpkg

func f(a bool) {
	for {
		if a {
			break
		} else {
			continue
		}
	}
}
`
	stmt, info := parseBody(t, src)

	loop, ok := stmt.(*ast.ForStmt)
	if !ok {
		t.Fatalf("expected the last statement to be a for loop, got %T", stmt)
	}

	ifStmt, ok := loop.Body.List[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected an if statement in the loop body, got %T", loop.Body.List[0])
	}

	if !stmtfacts.Terminates(info, ifStmt.Body.List[0]) {
		t.Error("Terminates(break) = false, want true")
	}

	elseBlock := ifStmt.Else.(*ast.BlockStmt)
	if !stmtfacts.Terminates(info, elseBlock.List[0]) {
		t.Error("Terminates(continue) = false, want true")
	}
	// And so the if-else as a whole hands control away, which is what lets a
	// caller drop an else that follows it.
	if !stmtfacts.Terminates(info, ifStmt) {
		t.Error("Terminates(if break else continue) = false, want true")
	}
}

// Terminates must reach the same verdict without type information, since
// analyzers run over packages that were never type-checked. Only the shadowed
// panic case legitimately differs: without types the name is all there is, and
// the fallback deliberately errs towards treating it as the builtin.
func TestTerminatesWithoutTypeInfo(t *testing.T) {
	src := `package testpkg

func f(a bool) int {
	if a {
		return 1
	} else {
		return 2
	}
}
`

	stmt, _ := parseBody(t, src)
	if !stmtfacts.Terminates(nil, stmt) {
		t.Error("Terminates(nil, ...) = false, want true for an if-else where both arms return")
	}
}

func TestDeclaredNames(t *testing.T) {
	src := `package testpkg

func f() {
	a := 1
	var b int
	const c = 2
	type d struct{}
	_, e := 3, 4
	g = 5
	if h := 6; h > 0 {
		i := 7
		_ = i
	}
	_, _, _, _, _ = a, b, c, e, g
	var _ d
}
`
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	fn := file.Decls[0].(*ast.FuncDecl)

	got := stmtfacts.DeclaredNames(fn.Body.List)

	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if !got[name] {
			t.Errorf("DeclaredNames() is missing %q", name)
		}
	}
	// g is assigned, not declared; h and i belong to the if's own scopes and
	// travel with it; the blank identifier declares nothing.
	for _, name := range []string{"g", "h", "i", "_"} {
		if got[name] {
			t.Errorf("DeclaredNames() wrongly includes %q", name)
		}
	}
}

func TestSignatureNames(t *testing.T) {
	src := `package testpkg

type T struct{}

func (r *T) f(a, b int) (res error) {
	return nil
}
`
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var fn *ast.FuncDecl

	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name.Name == "f" {
			fn = d
		}
	}

	got := stmtfacts.SignatureNames(fn.Recv, fn.Type)
	for _, name := range []string{"r", "a", "b", "res"} {
		if !got[name] {
			t.Errorf("SignatureNames() is missing %q", name)
		}
	}
}

func TestFirstClash(t *testing.T) {
	moved := map[string]bool{"z": true, "a": true, "q": true}
	dest := map[string]bool{"q": true, "z": true}

	// Sorted, so the reported name does not depend on map iteration order.
	got := stmtfacts.FirstClash(moved, dest)
	if got != "q" {
		t.Errorf("FirstClash() = %q, want %q", got, "q")
	}

	got = stmtfacts.FirstClash(map[string]bool{"a": true}, dest)
	if got != "" {
		t.Errorf("FirstClash() = %q, want %q for disjoint sets", got, "")
	}
}

func TestReferencesAny(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want bool
	}{
		{name: "bare identifier", expr: "ok", want: true},
		{name: "inside a call", expr: "fmt.Errorf(\"%v\", ok)", want: true},
		{name: "as a selector operand", expr: "ok.Field", want: true},
		{name: "unrelated", expr: "other", want: false},
		// The field name is not the variable, so a field that happens to share
		// the name must not count as a reference to it.
		{name: "as a selector field", expr: "other.ok", want: false},
		{name: "nested selector operand", expr: "a.b(ok.c).d", want: true},
		{name: "nested selector field only", expr: "a.b(c.ok).d", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := parser.ParseExpr(tt.expr)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			got := stmtfacts.ReferencesAny(expr, map[string]bool{"ok": true})
			if got != tt.want {
				t.Errorf("ReferencesAny(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestProvablyNonNil(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"guarded return", `if err != nil { return err }`, true},
		{"assigned before the return", `if err != nil { err = nil; return err }`, false},
		{"closure parameter redeclares the name", `if err != nil { h := func(err error) error { return err }; _ = h }`, false},
		{"closure runs later", `if err != nil { h := func() error { return err }; _ = h }`, false},
		{"loop writes the name after the return", `if err != nil { for i := 0; i < 3; i++ { if i > 1 { return err }; err = nil } }`, false},
		{"loop that never writes it", `if err != nil { for i := 0; i < 3; i++ { if i > 1 { return err } } }`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\nfunc f(err error) error {\n" + tc.body + "\nreturn nil\n}\n"

			file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var stack, found []ast.Node
			var ret *ast.ReturnStmt

			ast.Inspect(file, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}

				stack = append(stack, n)
				// The innermost return of the guarded branch is the one
				// asked about; the trailing `return nil` is not.
				r, ok := n.(*ast.ReturnStmt)
				if ok && len(r.Results) == 1 {
					id, ok := r.Results[0].(*ast.Ident)
					if ok && id.Name == "err" {
						ret, found = r, append([]ast.Node(nil), stack...)
					}
				}

				return true
			})

			if ret == nil {
				t.Fatal("no return of err in the fixture")
			}

			got := stmtfacts.ProvablyNonNil(found, ret, "err")
			if got != tc.want {
				t.Errorf("ProvablyNonNil = %v, want %v", got, tc.want)
			}
		})
	}
}
