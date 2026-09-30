package refactor

import (
	"fmt"
	"go/scanner"
	"go/token"
	"maps"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// reach is what one view's run touched, as far as coverage reporting needs it.
type reach struct {
	// dirs holds the directories of the packages the run visited or changed.
	dirs []string
	// names holds the names of the symbols the plan changes, as written at
	// their use sites.
	names []string
}

// reached returns the union of the views' reach.
func (r *configRun) reached() (dirs []string, names map[string]bool) {
	seen := make(map[string]bool)
	names = make(map[string]bool)
	for _, res := range r.results {
		for _, dir := range res.reach.dirs {
			seen[dir] = true
		}
		for _, name := range res.reach.names {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), names
}

// reportUncovered lists on plan the files in reached packages that no view
// type-checks, with a skipped site for each one that names a changed symbol.
// It fails when a view cannot say which test files of a reached package it
// left out.
func (r *configRun) reportUncovered(plan *types.RefactoringPlan) error {
	dirs, names := r.reached()
	if err := r.unattributedAbort(dirs); err != nil {
		return err
	}
	for _, u := range analysis.UncheckedFiles(r.views, dirs) {
		file := types.UncoveredFile{Path: u.Source.Path, Configs: configNames(u.Configs), Reason: u.Reason}
		plan.Coverage.Uncovered = append(plan.Coverage.Uncovered, file)
		if site, ok := mentionSite(u.Source, names, file); ok {
			plan.SkippedSites = append(plan.SkippedSites, site)
		}
	}
	return nil
}

// unattributedAbort fails when any view dropped every test file of a package
// in dirs, whether or not that view's own run reached the package.
func (r *configRun) unattributedAbort(dirs []string) error {
	for _, v := range r.views {
		var blind []*types.Package
		for _, dir := range dirs {
			if pkg := v.Workspace.Packages[dir]; pkg != nil && pkg.UnattributedTestErr != nil {
				blind = append(blind, pkg)
			}
		}
		if len(blind) > 0 {
			return r.configError(v, unattributedTestsError(blind))
		}
	}
	return nil
}

// unattributedTestsError is the abort for packages whose test files were all
// dropped from type-checking without a per-file cause.
func unattributedTestsError(pkgs []*types.Package) error {
	var b strings.Builder
	fmt.Fprintf(&b,
		"%d package(s) reached by this operation had every _test.go file left out of type-checking, because "+
			"dropping the files that carry errors did not leave the rest error-free. Which of those files "+
			"reference the changed symbols cannot be told, so proceeding could leave them uncompilable "+
			"without saying where:\n", len(pkgs))
	for _, pkg := range pkgs {
		fmt.Fprintf(&b, "  - %s (%d test file(s)): %v\n", pkg.ImportPath, len(pkg.ExcludedTestFiles), pkg.UnattributedTestErr)
	}
	b.WriteString("fix the type errors in those test files and re-run")
	return &types.RefactorError{Type: types.InvalidOperation, Message: b.String()}
}

func configNames(configs []analysis.BuildConfig) []string {
	if len(configs) == 0 {
		return nil
	}
	names := make([]string, len(configs))
	for i, c := range configs {
		names[i] = c.String()
	}
	return names
}

// mentionSite returns a skipped site at the first identifier in src that is
// one of names. Comments and string literals do not count.
func mentionSite(src *analysis.SourceFile, names map[string]bool, file types.UncoveredFile) (types.SkippedSite, bool) {
	if len(names) == 0 || len(src.Content) == 0 {
		return types.SkippedSite{}, false
	}
	tf := token.NewFileSet().AddFile(src.Path, -1, len(src.Content))
	var s scanner.Scanner
	s.Init(tf, src.Content, nil, 0)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return types.SkippedSite{}, false
		}
		if tok != token.IDENT || !names[lit] {
			continue
		}
		return types.SkippedSite{
			File:    src.Path,
			Line:    tf.Line(pos),
			Reason:  mentionReason(lit, file),
			Snippet: lineAt(src.Content, tf.Offset(pos)),
		}, true
	}
}

func mentionReason(name string, file types.UncoveredFile) string {
	where := "no build configuration includes the file"
	if len(file.Configs) > 0 {
		where = "not type-checked under " + strings.Join(file.Configs, ", ")
	}
	return fmt.Sprintf("mentions %s but was not rewritten: %s (%s); update it by hand", name, where, file.Reason)
}

// lineAt returns the line of content that holds offset, without surrounding space.
func lineAt(content []byte, offset int) string {
	start := strings.LastIndexByte(string(content[:offset]), '\n') + 1
	end := len(content)
	if i := strings.IndexByte(string(content[offset:]), '\n'); i >= 0 {
		end = offset + i
	}
	return strings.TrimSpace(string(content[start:end]))
}
