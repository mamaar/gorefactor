package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// findCallSiteIdentPos walks the AST and returns the position of the nth (1-based)
// *ast.Ident with the given name that appears as a SelectorExpr.Sel (i.e., a call site),
// skipping FuncDecl names.
func findCallSiteIdentPos(astFile *ast.File, name string, nth int) token.Pos {
	count := 0
	var result token.Pos
	ast.Inspect(astFile, func(n ast.Node) bool {
		if result.IsValid() {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == name {
			count++
			if count == nth {
				result = sel.Sel.Pos()
				return false
			}
		}
		return true
	})
	return result
}

func TestContainsReference_GoroutineClosure(t *testing.T) {
	src := `package main

type Server struct{}

func (s *Server) Handle(k int) {
	go func(k int) {
		s.Process(k)
	}(k)
}

func (s *Server) Process(k int) {}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "server.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path:            "/test/server.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	ws := &types.Workspace{
		FileSet: fset,
		Packages: map[string]*types.Package{
			"main": {
				Path: "main",
				Files: map[string]*types.File{
					"server.go": file,
				},
				TestFiles: map[string]*types.File{},
			},
		},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Server.Process",
		SourceFile:   "/test/server.go",
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "k", Type: "int"},
		},
		DefaultValue:     "context.TODO()",
		NewParamPosition: 0,
	}

	refPos := findCallSiteIdentPos(astFile, "Process", 1)
	if !refPos.IsValid() {
		t.Fatal("could not find reference position for Process")
	}

	ref := &types.Reference{
		File:     "/test/server.go",
		Position: refPos,
	}

	changes := op.updateCallSite(ref, ws)

	if len(changes) == 0 {
		t.Fatal("expected at least 1 change, got 0")
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly 1 change, got %d", len(changes))
	}

	c := changes[0]

	// OldText should be just "s.Process(k)", not the entire goroutine closure
	expectedOld := "s.Process(k)"
	if c.OldText != expectedOld {
		t.Errorf("OldText: expected %q, got %q", expectedOld, c.OldText)
	}

	expectedNew := "s.Process(context.TODO(), k)"
	if c.NewText != expectedNew {
		t.Errorf("NewText: expected %q, got %q", expectedNew, c.NewText)
	}
}

func TestUpdateCallSite_GoroutineClosurePreservesWrapper(t *testing.T) {
	src := `package main

import "sync"

type Server struct{}

func (s *Server) Handle() {
	var wg sync.WaitGroup
	for k := 0; k < 10; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			resp, err := s.Process(k, "hello")
			_ = resp
			_ = err
		}(k)
	}
	wg.Wait()
}

func (s *Server) Process(k int, msg string) (string, error) {
	return "", nil
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "server.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path:            "/test/server.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	ws := &types.Workspace{
		FileSet: fset,
		Packages: map[string]*types.Package{
			"main": {
				Path: "main",
				Files: map[string]*types.File{
					"server.go": file,
				},
				TestFiles: map[string]*types.File{},
			},
		},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Server.Process",
		SourceFile:   "/test/server.go",
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "k", Type: "int"},
			{Name: "msg", Type: "string"},
		},
		NewReturns:       []string{"string", "error"},
		DefaultValue:     "context.TODO()",
		NewParamPosition: 0,
	}

	refPos := findCallSiteIdentPos(astFile, "Process", 1)
	if !refPos.IsValid() {
		t.Fatal("could not find Process call site position")
	}

	ref := &types.Reference{
		File:     "/test/server.go",
		Position: refPos,
	}

	changes := op.updateCallSite(ref, ws)

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}

	c := changes[0]

	// Verify OldText is exactly the call expression from source
	expectedOld := `s.Process(k, "hello")`
	if c.OldText != expectedOld {
		t.Errorf("OldText: expected %q, got %q", expectedOld, c.OldText)
	}

	// Verify new call has context.TODO() inserted at position 0
	expectedNew := `s.Process(context.TODO(), k, "hello")`
	if c.NewText != expectedNew {
		t.Errorf("NewText: expected %q, got %q", expectedNew, c.NewText)
	}

	// Verify the change range doesn't encompass the goroutine wrapper
	changeText := string(file.OriginalContent[c.Start:c.End])
	if changeText != expectedOld {
		t.Errorf("change range covers wrong text: %q", changeText)
	}
}

func TestUpdateCallSite_FuncLitArgPreserved(t *testing.T) {
	src := `package main

type Server struct{}

func (s *Server) Handle() {
	s.Execute("test", func(x int) bool {
		return x > 0
	})
}

func (s *Server) Execute(name string, fn func(int) bool) {}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "server.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path:            "/test/server.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	ws := &types.Workspace{
		FileSet: fset,
		Packages: map[string]*types.Package{
			"main": {
				Path: "main",
				Files: map[string]*types.File{
					"server.go": file,
				},
				TestFiles: map[string]*types.File{},
			},
		},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Server.Execute",
		SourceFile:   "/test/server.go",
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "name", Type: "string"},
			{Name: "fn", Type: "func(int) bool"},
		},
		DefaultValue:     "context.TODO()",
		NewParamPosition: 0,
	}

	refPos := findCallSiteIdentPos(astFile, "Execute", 1)
	if !refPos.IsValid() {
		t.Fatal("could not find Execute call site position")
	}

	ref := &types.Reference{
		File:     "/test/server.go",
		Position: refPos,
	}

	changes := op.updateCallSite(ref, ws)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}

	c := changes[0]

	// The FuncLit arg should be preserved exactly from source, not "func(...){...}"
	if c.NewText == "" {
		t.Fatal("NewText is empty")
	}

	// Verify the func lit content is preserved in the new text
	if !strings.Contains(c.NewText, "func(x int) bool") {
		t.Errorf("NewText should contain original func literal, got: %q", c.NewText)
	}

	// Verify OldText matches source exactly
	sourceText := string(file.OriginalContent[c.Start:c.End])
	if c.OldText != sourceText {
		t.Errorf("OldText %q doesn't match source text %q", c.OldText, sourceText)
	}
}
