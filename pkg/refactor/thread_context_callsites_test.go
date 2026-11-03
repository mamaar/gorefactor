package refactor

import (
	"errors"
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

func TestCallSiteSeed_TestFileSiteHasNoSeed(t *testing.T) {
	op := &ThreadContextOperation{}
	site := analysis.CallSite{File: &pkgtypes.File{Path: "x_test.go"}}
	seed, err := op.callSiteSeed(nil, nil, site)
	if seed != nil || !errors.Is(err, errNoSeed) {
		t.Errorf("callSiteSeed = %v, %v; want nil, errNoSeed", seed, err)
	}
}

func TestExtractImportsChange_NoImportsIsErrNoImportChange(t *testing.T) {
	change, err := extractImportsChange(nil, nil, nil)
	if change != nil || !errors.Is(err, errNoImportChange) {
		t.Errorf("extractImportsChange = %v, %v; want nil, errNoImportChange", change, err)
	}
}

func TestExtractImportsChange_AlreadyImportedIsErrNoImportChange(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\n\nimport \"errors\"\n", parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f := &pkgtypes.File{Path: "x.go", AST: file}
	change, err := extractImportsChange(nil, f, map[string]string{"errors": "errors"})
	if change != nil || !errors.Is(err, errNoImportChange) {
		t.Errorf("extractImportsChange = %v, %v; want nil, errNoImportChange", change, err)
	}
}
