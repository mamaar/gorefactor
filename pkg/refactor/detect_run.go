package refactor

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// DetectHeader is what the declaration detectors report about the build
// configurations they ran under.
type DetectHeader struct {
	Configs []string // display names, primary first
	// Unchecked holds the files no configuration type-checks: their
	// declarations and calls are missing.
	Unchecked []analysis.UncheckedFile
	// TypeErrors holds the non-test files with a type error under some
	// configuration: calls in their erroring expressions may be missing.
	TypeErrors []analysis.ProductionError
}

// detectRun is what the declaration detectors gather the same way in each view.
type detectRun struct {
	viewIndexes
	// packages keeps the declarations whose package is one of these import
	// paths or directories; empty keeps every package.
	packages   []string
	typeErrors map[string]analysis.ProductionError
	// signatures holds the signatures each function is declared with.
	signatures map[declKey]map[string]bool
}

func newDetectRun(packages []string) detectRun {
	return detectRun{
		packages:   packages,
		typeErrors: make(map[string]analysis.ProductionError),
		signatures: make(map[declKey]map[string]bool),
	}
}

// recordSignature notes a signature the function key is declared with.
func (r *detectRun) recordSignature(key declKey, sig string) {
	if r.signatures[key] == nil {
		r.signatures[key] = make(map[string]bool)
	}
	r.signatures[key][sig] = true
}

// declShape is the signature a declaration is compared by under change_signature:
// its parameter names and types and its result types.
func declShape(decl *funcDecl) string {
	return signatureOf(decl.Decl.Type).String()
}

// signatureRefusal returns why the declaration of function in file, with shape
// sig, is refused: another configuration declares it with a different shape.
func (r *detectRun) signatureRefusal(key declKey, function, file, sig string) string {
	var others []string
	for other := range r.signatures[key] {
		if other != sig {
			others = append(others, other)
		}
	}
	if len(others) == 0 {
		return ""
	}
	return fmt.Sprintf("%s in %s has signature %s, which differs from %s, the signature it is changed from under another build configuration; make the declarations agree first",
		function, file, sig, slices.Min(others))
}

func (r *detectRun) wanted(pkg *types.Package) bool {
	return len(r.packages) == 0 ||
		slices.Contains(r.packages, pkg.ImportPath) || slices.Contains(r.packages, pkg.Path)
}

// collectTypeErrors records the first type error of each non-test file of v.
func (r *detectRun) collectTypeErrors(v *analysis.View) {
	for _, e := range v.ProductionErrors() {
		if _, seen := r.typeErrors[e.File.Path]; !seen {
			r.typeErrors[e.File.Path] = e
		}
	}
}

func (r *detectRun) header(views []*analysis.View) DetectHeader {
	h := DetectHeader{
		Configs:    make([]string, len(views)),
		Unchecked:  analysis.UncheckedFiles(views, views[0].SourceDirs()),
		TypeErrors: slices.Collect(maps.Values(r.typeErrors)),
	}
	for i, v := range views {
		h.Configs[i] = v.Config.String()
	}
	slices.SortFunc(h.TypeErrors, func(a, b analysis.ProductionError) int {
		return cmp.Compare(a.File.Path, b.File.Path)
	})
	return h
}
