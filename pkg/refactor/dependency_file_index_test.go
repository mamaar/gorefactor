package refactor

import (
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestBuildFilePackageIndex_KeysByAbsoluteFilePath(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"src/src.go":      "package src\n\nfunc Shared() int { return 1 }\n",
		"src/src_test.go": "package src\n\nimport \"testing\"\n\nfunc TestShared(t *testing.T) {}\n",
	})
	eng.(*DefaultEngine).EnsureTypeChecked(ws)

	index := buildFilePackageIndex(ws)

	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			if index[f.Path] != pkg {
				t.Errorf("index[%s] = %v, want its package", f.Path, index[f.Path])
			}
		}
		for _, f := range pkg.TestFiles {
			if index[f.Path] != pkg {
				t.Errorf("index[%s] = %v, want its package", f.Path, index[f.Path])
			}
		}
	}
}

func TestSuggestedMoves_FindsSymbolReferencedByTwoPackages(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Shared() int { return 1 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/extracttest/src\"\n\nfunc A() int { return src.Shared() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/extracttest/src\"\n\nfunc B() int { return src.Shared() }\n",
	})
	eng.(*DefaultEngine).EnsureTypeChecked(ws)

	moves := (&AnalyzeDependenciesOperation{}).generateSuggestedMoves(ws)

	if len(moves) != 1 || moves[0].Symbol != "Shared" {
		t.Errorf("moves = %+v, want one suggestion for Shared", moves)
	}
}

func TestAnalyzeDependencies_SuggestsDotImportedSymbolOnUntypeCheckedWorkspace(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Shared() int { return 1 }\n",
		"a/a.go":     "package a\n\nimport . \"example.com/extracttest/src\"\n\nfunc A() int { return Shared() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/extracttest/src\"\n\nfunc B() int { return src.Shared() }\n",
	})

	plan, err := eng.AnalyzeDependencies(ws, types.AnalyzeDependenciesRequest{Workspace: ws.RootPath, SuggestMoves: true})
	if err != nil {
		t.Fatal(err)
	}

	moves := plan.Impact.SuggestedMoves
	if len(moves) != 1 || moves[0].Symbol != "Shared" || len(moves[0].ReferencingPackages) != 2 {
		t.Errorf("suggested moves = %+v, want Shared referenced by two packages", moves)
	}
}
