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
	m, ok := firstMention(src, names)
	if !ok {
		return types.SkippedSite{}, false
	}
	return types.SkippedSite{File: src.Path, Line: m.Line, Reason: mentionReason(m.name, file), Snippet: m.Context}, true
}

// mention is an identifier found in a file by name alone.
type mention struct {
	types.Reference
	name string
}

// firstMention returns the first identifier in src that is one of names.
func firstMention(src *analysis.SourceFile, names map[string]bool) (mention, bool) {
	found := mentions(src, names, true)
	if len(found) == 0 {
		return mention{}, false
	}
	return found[0], true
}

// mentions returns the first identifier in src for each of names, in source
// order, or only the first of all. Comments and string literals do not count.
func mentions(src *analysis.SourceFile, names map[string]bool, firstOnly bool) []mention {
	if len(names) == 0 || len(src.Content) == 0 {
		return nil
	}
	tf := token.NewFileSet().AddFile(src.Path, -1, len(src.Content))
	var s scanner.Scanner
	s.Init(tf, src.Content, nil, 0)
	var found []mention
	seen := make(map[string]bool)
	for len(seen) < len(names) {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.IDENT || !names[lit] || seen[lit] {
			continue
		}
		seen[lit] = true
		at := tf.Position(pos)
		found = append(found, mention{name: lit, Reference: types.Reference{
			File:    src.Path,
			Offset:  at.Offset,
			Line:    at.Line,
			Column:  at.Column,
			Context: lineAt(src.Content, at.Offset),
		}})
		if firstOnly {
			break
		}
	}
	return found
}

func mentionReason(name string, file types.UncoveredFile) string {
	return fmt.Sprintf("mentions %s but was not rewritten: %s; update it by hand", name, uncheckedWhere(file))
}

// uncheckedWhere says under which configurations file went unchecked, and why.
func uncheckedWhere(file types.UncoveredFile) string {
	where := "no build configuration includes the file"
	if len(file.Configs) > 0 {
		where = "not type-checked under " + strings.Join(file.Configs, ", ")
	}
	return fmt.Sprintf("%s (%s)", where, file.Reason)
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

// blockers returns the blockers of every view, each once and with the
// configurations that see it, followed by the uses no view could type-check.
func (r *configRun) blockers() []types.Blocker {
	unverified := unverifiedUses(r.views, r.linked)
	unchecked := make(map[string]bool, len(unverified))
	for _, b := range unverified {
		unchecked[b.File] = true
	}

	type site struct {
		file   string
		offset int
	}
	var out []types.Blocker
	var seenBy [][]string
	at := make(map[site]int)
	for i, res := range r.results {
		name := r.views[i].Config.String()
		for _, b := range res.blockers {
			if unchecked[b.File] {
				continue
			}
			key := site{b.File, b.Offset}
			j, ok := at[key]
			if !ok {
				j = len(out)
				at[key] = j
				out = append(out, b)
				seenBy = append(seenBy, nil)
			}
			if !slices.Contains(seenBy[j], name) {
				seenBy[j] = append(seenBy[j], name)
			}
		}
	}
	for j := range out {
		if len(seenBy[j]) < len(r.views) {
			out[j].Configs = seenBy[j]
		}
	}
	return append(out, unverified...)
}

// unverifiedOf returns the blockers known by name only.
func unverifiedOf(blockers []types.Blocker) []types.Blocker {
	var out []types.Blocker
	for _, b := range blockers {
		if b.Unverified != "" {
			out = append(out, b)
		}
	}
	return out
}

// unverifiedUses returns a blocker at the first identifier of each file no
// view type-checks that could be a use of a symbol in keys.
func unverifiedUses(views []*analysis.View, keys types.SymbolKeySet) []types.Blocker {
	var out []types.Blocker
	for _, m := range uncheckedMentions(views, keys) {
		if len(out) == 0 || out[len(out)-1].File != m.site.File {
			out = append(out, m.site)
		}
	}
	return out
}

// uncheckedMention is the first place a file no view type-checks names a
// symbol it could be using.
type uncheckedMention struct {
	key  types.SymbolKey
	site types.Blocker
}

// uncheckedMentions returns, file by file and in source order, the mentions
// of the symbols in keys in the files no view type-checks.
func uncheckedMentions(views []*analysis.View, keys types.SymbolKeySet) []uncheckedMention {
	if len(keys) == 0 {
		return nil
	}
	dirs := declaringDirs(views, keys)
	ordered := orderedKeys(keys)
	var out []uncheckedMention
	for _, u := range analysis.UncheckedFiles(views, views[0].SourceDirs()) {
		names := make(map[string]bool)
		byName := make(map[string][]types.SymbolKey)
		for _, key := range ordered {
			if mayUse(u.Source, key, dirs[key]) {
				names[key.Name] = true
				byName[key.Name] = append(byName[key.Name], key)
			}
		}
		for _, m := range mentions(u.Source, names, false) {
			site := types.Blocker{Reference: m.Reference, Configs: configNames(u.Configs), Unverified: u.Reason}
			for _, key := range byName[m.name] {
				out = append(out, uncheckedMention{key: key, site: site})
			}
		}
	}
	return out
}

// declaringDirs returns the directory of the package that declares each key.
func declaringDirs(views []*analysis.View, keys types.SymbolKeySet) map[types.SymbolKey]string {
	dirs := make(map[types.SymbolKey]string, len(keys))
	for key := range keys {
		for _, v := range views {
			if pkg := resolveSymbolPackage(v.Workspace, &types.Symbol{Package: key.PkgPath}); pkg != nil {
				dirs[key] = pkg.Dir
				break
			}
		}
	}
	return dirs
}

// mayUse reports whether f, which is not type-checked, could use the symbol
// key names, declared in dir: f is in dir, or the symbol is exported and f
// imports its package. An exported method or field is reachable from anywhere.
func mayUse(f *analysis.SourceFile, key types.SymbolKey, dir string) bool {
	if f.Dir == dir {
		return true
	}
	if !token.IsExported(key.Name) {
		return false
	}
	if key.Recv != "" || f.AST == nil {
		return true
	}
	for _, imp := range f.AST.Imports {
		if strings.Trim(imp.Path.Value, "\"`") == key.PkgPath {
			return true
		}
	}
	return false
}

// reportUnverified lists on plan the unverified uses in files it neither
// lists yet nor changes, each with a skipped site.
func reportUnverified(plan *types.RefactoringPlan, unverified []types.Blocker) {
	listed := make(map[string]bool, len(plan.Coverage.Uncovered))
	for _, u := range plan.Coverage.Uncovered {
		listed[u.Path] = true
	}
	for _, c := range plan.Changes {
		listed[c.File] = true
	}
	for _, b := range unverified {
		if listed[b.File] {
			continue
		}
		file := types.UncoveredFile{Path: b.File, Configs: b.Configs, Reason: b.Unverified}
		plan.Coverage.Uncovered = append(plan.Coverage.Uncovered, file)
		plan.SkippedSites = append(plan.SkippedSites, types.SkippedSite{
			File:    b.File,
			Line:    b.Line,
			Reason:  "may use a symbol this operation changes: " + uncheckedWhere(file) + "; update it by hand",
			Snippet: b.Context,
		})
	}
}

// reportTypeErrors lists on plan, per view, the packages with type errors in
// non-test files that the run reached or whose erroring files name a changed
// symbol.
func (r *configRun) reportTypeErrors(plan *types.RefactoringPlan) {
	dirs, names := r.reached()
	for key := range r.linked {
		names[key.Name] = true
	}
	for _, v := range r.views {
		if v.Parser == nil {
			continue
		}
		listed := make(map[*types.Package]bool)
		for _, e := range v.ProductionErrors() {
			if listed[e.Package] || !(slices.Contains(dirs, e.Package.Dir) || namesAny(e.File, names)) {
				continue
			}
			listed[e.Package] = true
			plan.Coverage.TypeErrors = append(plan.Coverage.TypeErrors, types.PackageTypeError{
				Package: e.Package.ImportPath,
				Config:  v.Config.String(),
				Error:   e.Err.Error(),
			})
		}
	}
}

// namesAny reports whether an identifier in file is one of names.
func namesAny(file *types.File, names map[string]bool) bool {
	_, ok := firstMention(&analysis.SourceFile{Path: file.Path, Content: file.OriginalContent}, names)
	return ok
}
