package analysis

import (
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// UncheckedFile is a file that no view has type information for.
type UncheckedFile struct {
	Source  *SourceFile
	Configs []BuildConfig // the views that include the file; none when no configuration can
	Reason  string
}

// UncheckedFiles returns, in path order, the files directly in dirs that no
// configuration can include or that every view including them left out of
// type-checking. A file one view excludes and another checks is not returned.
func UncheckedFiles(views []*View, dirs []string) []UncheckedFile {
	if len(views) == 0 || len(dirs) == 0 {
		return nil
	}
	tree := views[0].Parser.sources
	dirs = slices.Clone(dirs)
	slices.Sort(dirs)

	var out []UncheckedFile
	for _, dir := range slices.Compact(dirs) {
		for _, f := range tree.Dir(dir) {
			if u, ok := uncheckedFile(views, f); ok {
				out = append(out, u)
			}
		}
	}
	return out
}

// SourceDirs returns every directory of the source tree v projects, sorted.
func (v *View) SourceDirs() []string { return v.Parser.sources.Dirs() }

func uncheckedFile(views []*View, f *SourceFile) (UncheckedFile, bool) {
	u := UncheckedFile{Source: f}
	if f.Err != nil {
		u.Reason = "parse error: " + f.Err.Error()
		return u, true
	}

	var reasons []string
	for _, v := range views {
		if !v.Config.MatchesFile(f) {
			continue
		}
		reason := v.uncheckedReason(f)
		if reason == "" {
			return UncheckedFile{}, false
		}
		u.Configs = append(u.Configs, v.Config)
		reasons = append(reasons, reason)
	}
	if len(reasons) == 0 {
		u.Reason = staticReason(f, views[0].Config)
		return u, true
	}
	u.Reason = joinReasons(u.Configs, reasons)
	return u, true
}

// uncheckedReason returns why v has no type information for f, which its
// configuration includes, or "" when it has.
func (v *View) uncheckedReason(f *SourceFile) string {
	pkg := v.Workspace.Packages[f.Dir]
	if pkg == nil {
		return "the directory holds no package under this configuration"
	}
	v.Parser.mu.Lock()
	defer v.Parser.mu.Unlock()
	if !pkg.TypesChecked {
		return "the package was not type-checked"
	}
	if err := pkg.ExcludedTestFiles[f.Name]; err != nil {
		return err.Error()
	}
	return ""
}

// ExcludedTestFiles returns the paths of the test files v includes that
// type-checking left out.
func (v *View) ExcludedTestFiles() map[string]bool {
	v.Parser.mu.Lock()
	defer v.Parser.mu.Unlock()
	paths := make(map[string]bool)
	for _, pkg := range v.Workspace.Packages {
		for name := range pkg.ExcludedTestFiles {
			if f := pkg.TestFiles[name]; f != nil {
				paths[f.Path] = true
			}
		}
	}
	return paths
}

// staticReason says why no configuration includes f.
func staticReason(f *SourceFile, primary BuildConfig) string {
	_, u := requirementFor(f, primary)
	switch {
	case u == nil:
		return "no build configuration of the covering set includes the file"
	case u.Constraint == "" || u.Reason == ignoreReason:
		return u.Reason
	default:
		return u.Reason + ": " + u.Constraint
	}
}

// joinReasons returns the one reason all configurations share, or each
// prefixed with its configuration.
func joinReasons(configs []BuildConfig, reasons []string) string {
	if len(slices.Compact(slices.Clone(reasons))) == 1 {
		return reasons[0]
	}
	parts := make([]string, len(reasons))
	for i, reason := range reasons {
		parts[i] = configs[i].String() + ": " + reason
	}
	return strings.Join(parts, "; ")
}

// ProductionError is the first type error located in one non-test file.
type ProductionError struct {
	Package *types.Package
	File    *types.File
	Err     error
}

// ProductionErrors returns the first type error of each non-test file the
// last type-check of v found one in, ordered by path.
func (v *View) ProductionErrors() []ProductionError {
	v.Parser.mu.Lock()
	defer v.Parser.mu.Unlock()
	var out []ProductionError
	for _, pkg := range v.Workspace.Packages {
		for name, err := range pkg.ProductionErrs {
			if f := pkg.Files[name]; f != nil {
				out = append(out, ProductionError{Package: pkg, File: f, Err: err})
			}
		}
	}
	slices.SortFunc(out, compareProductionErrors)
	return out
}

func compareProductionErrors(a, b ProductionError) int {
	return strings.Compare(a.File.Path, b.File.Path)
}
