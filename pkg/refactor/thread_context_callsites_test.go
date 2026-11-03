package refactor

import (
	"errors"
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
