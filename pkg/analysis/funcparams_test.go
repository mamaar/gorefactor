package analysis

import (
	"errors"
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestExtractFuncParams_MissingTargetIsSymbolNotFound(t *testing.T) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "a.go", "package p\n\nfunc f(n int) {}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	file := &types.File{Path: "a.go", AST: astFile}
	ws := &types.Workspace{Packages: map[string]*types.Package{"p": {Files: map[string]*types.File{"a.go": file}}}}

	tests := []struct {
		name, file, fn, msg string
	}{
		{"function", "a.go", "g", "function g not found in a.go"},
		{"file", "b.go", "f", "source file not found: b.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExtractFuncParams(ws, tt.file, tt.fn)
			var re *types.RefactorError
			if !errors.As(err, &re) || re.Type != types.SymbolNotFound {
				t.Fatalf("err = %#v, want a SymbolNotFound RefactorError", err)
			}
			if err.Error() != tt.msg {
				t.Errorf("message = %q, want %q", err.Error(), tt.msg)
			}
		})
	}
}
