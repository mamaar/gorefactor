package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestRemoveImportSpecs_DropsTheDeclarationItEmpties(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		remove    []string
		want      string
	}{
		{"only spec of a block", "package p\n\nimport (\n\t\"fmt\"\n)\n\nvar X = 1\n", []string{"fmt"}, "package p\n\nvar X = 1\n"},
		{"every spec of a block", "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar X = 1\n", []string{"fmt", "os"}, "package p\n\nvar X = 1\n"},
		{"one spec of a block", "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar X = os.Args\n", []string{"fmt"}, "package p\n\nimport (\n\t\"os\"\n)\n\nvar X = os.Args\n"},
		{"single-line import", "package p\n\nimport \"fmt\"\n\nvar X = 1\n", []string{"fmt"}, "package p\n\nvar X = 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()

			af, err := parser.ParseFile(fset, "p.go", tc.src, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}

			ws := &types.Workspace{FileSet: fset}
			file := &types.File{Path: "p.go", AST: af, OriginalContent: []byte(tc.src)}
			var specs []*ast.ImportSpec

			for _, imp := range af.Imports {
				for _, path := range tc.remove {
					if imp.Path.Value == `"`+path+`"` {
						specs = append(specs, imp)
					}
				}
			}

			got := string(applyChangesInMemory(file.OriginalContent, removeImportSpecs(ws, file, specs)))
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}
