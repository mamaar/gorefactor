// Package filedata provides a pass-through analyzer that makes raw file
// content available to downstream analyzers via pass.ResultOf.
//
// The runner pre-populates the result with a *Data value containing
// per-file byte slices sourced from the workspace.
package filedata

import (
	"reflect"

	"golang.org/x/tools/go/analysis"
)

// Data holds raw file content indexed by the filename registered in token.FileSet.
type Data struct {
	// Content maps token.Position.Filename → raw bytes.
	Content map[string][]byte
}

// Analyzer is a placeholder required by downstream analyzers so the runner
// can inject file content via pass.ResultOf[filedata.Analyzer].
var Analyzer = &analysis.Analyzer{
	Name:       "filedata",
	Doc:        "provides raw file content to downstream analyzers",
	Run:        run,
	ResultType: reflect.TypeOf((*Data)(nil)),
}

func run(pass *analysis.Pass) (any, error) {
	// In normal analysistest usage this returns empty data.
	// The runner overrides this via pass.ResultOf.
	return &Data{Content: make(map[string][]byte)}, nil
}
