package refactor

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// TypeCheckResult reports the outcome of verifying a plan against go/types.
type TypeCheckResult struct {
	// NewErrors are type errors introduced by the plan: errors present after
	// applying the changes that were not present before.
	NewErrors []string
	// Skipped is set with a reason when verification could not run at all —
	// or, for a plan touching several packages, could not run for one or
	// more of them — so callers know NewErrors is incomplete. Callers must
	// treat a skipped verification as "unverified", not as "verified clean":
	// OK() reports only on the packages that *were* checked, so a caller
	// that ignores Skipped can be fooled by a plan whose one broken package
	// happens to be the one nobody could check.
	Skipped string
}

// OK reports whether verification found no new type errors. It says nothing
// about packages recorded in Skipped — check that separately before trusting
// a true result as "verified clean".
func (r *TypeCheckResult) OK() bool { return len(r.NewErrors) == 0 }

// VerifyPlanTypesWithImporter applies plan's changes to in-memory copies of
// the affected files and type-checks the result, writing nothing to disk. It
// type-checks each touched package before and after the plan and reports only
// the errors that are new, so pre-existing type errors don't block a plan
// (see normalizeTypeError for how "new" is decided).
//
// Callers that already have a type-checked workspace must pass the importer
// that checked it: go/types compares packages by identity, so a second
// importer yields two distinct *types.Package values for the same import path
// and every cross-use between them looks like a type mismatch (regression
// test in verify_types_test.go). imp may be nil when nothing has type-checked
// ws yet; a fresh parser/importer is then built for this call only.
func VerifyPlanTypesWithImporter(ws *types.Workspace, plan *types.RefactoringPlan, imp gotypes.Importer) *TypeCheckResult {
	if ws == nil {
		return &TypeCheckResult{Skipped: "no workspace provided"}
	}

	if ws.FileSet == nil {
		return &TypeCheckResult{Skipped: "workspace has no FileSet"}
	}

	if plan == nil || len(plan.Changes) == 0 {
		// Nothing to apply, so nothing can have gone newly wrong.
		return &TypeCheckResult{}
	}

	changesByFile := make(map[string][]types.Change, len(plan.Changes))
	for _, c := range plan.Changes {
		changesByFile[c.File] = append(changesByFile[c.File], c)
	}

	// Map each changed file to the package that owns it, using the same
	// full-filesystem-path identity Change.File is populated with throughout
	// the operations in this package (see e.g. alias_operations.go,
	// bulk_operations.go: File: file.Path).
	fileToPkg := packagesByFile(ws)

	touched := make(map[*types.Package]bool)
	var untracked []string

	for file := range changesByFile {
		pkg, ok := fileToPkg[file]
		if ok {
			touched[pkg] = true
		} else {
			// Most commonly a file the plan creates from scratch (a move or
			// extract-to-new-file target). There is no baseline to diff
			// against for content that doesn't exist yet, so it is reported
			// as skipped rather than silently ignored.
			untracked = append(untracked, file)
		}
	}

	result := &TypeCheckResult{}
	var skipped []string

	if len(touched) > 0 {
		importer := imp
		if importer == nil {
			// No caller-supplied importer: bind a throwaway GoParser to ws
			// purely to obtain its workspace importer (dependency
			// resolution: workspace-local lookup, then go/packages-resolved
			// external modules, then importer.Default()). This does not
			// require ws to have been type-checked already — type-checking
			// is lazy (see (*analysis.GoParser).EnsureTypeChecked) — the
			// importer type-checks each workspace-local dependency on first
			// use via its own recursive session and memoizes the result
			// onto ws.Packages, exactly like a real TypeCheckPackage call
			// would. This path is only safe when nothing touched by the
			// plan depends on a package some *other* importer already
			// memoized — see VerifyPlanTypesWithImporter's doc comment.
			gp := analysis.NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
			gp.BindWorkspace(ws)
			importer = gp.Importer()
		}

		if importer == nil {
			return &TypeCheckResult{Skipped: "could not obtain a workspace importer"}
		}

		pkgs := make([]*types.Package, 0, len(touched))
		for pkg := range touched {
			pkgs = append(pkgs, pkg)
		}
		// Deterministic order so NewErrors/Skipped don't reshuffle between
		// runs over the same plan (touched is a map).
		sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ImportPath < pkgs[j].ImportPath })

		result.NewErrors, skipped = verifyTouched(ws, pkgs, changesByFile, importer)
	}

	if len(untracked) > 0 {
		sort.Strings(untracked)
		skipped = append(skipped, fmt.Sprintf(
			"%d changed file(s) not found in any workspace package (new files are not verified): %s",
			len(untracked), strings.Join(untracked, ", ")))
	}

	if len(skipped) > 0 {
		result.Skipped = strings.Join(skipped, "; ")
	}

	return result
}

func packagesByFile(ws *types.Workspace) map[string]*types.Package {
	fileToPkg := make(map[string]*types.Package)

	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			fileToPkg[f.Path] = pkg
		}

		for _, f := range pkg.TestFiles {
			fileToPkg[f.Path] = pkg
		}
	}

	return fileToPkg
}

// packageVerifyResult is verifyPackageTypes' internal result: either newErrors
// (verification ran) or skipped (it didn't, with a reason) — never both.
type packageVerifyResult struct {
	newErrors []string
	skipped   string
	// modified is the package as type-checked with the plan applied.
	modified *gotypes.Package
}

// overlayImporter resolves the packages already verified with the plan
// applied to their modified form and every other path through base, so a
// package that imports a touched one is checked against its post-plan API.
type overlayImporter struct {
	base gotypes.Importer
	over map[string]*gotypes.Package
}

func (o *overlayImporter) Import(path string) (*gotypes.Package, error) {
	p := o.over[path]
	if p != nil {
		return p, nil
	}

	return o.base.Import(path)
}

// changesProduction reports whether the plan edits a non-test file of pkg.
func changesProduction(pkg *types.Package, changesByFile map[string][]types.Change) bool {
	for _, f := range pkg.Files {
		if changesByFile[f.Path] != nil {
			return true
		}
	}

	return false
}

// verifyOrder says which packages are re-checked so importers see the plan's
// API, and in what order.
type verifyOrder struct {
	// overlaid lists the packages whose non-test files change, and those that
	// import one of them directly or through other workspace packages,
	// dependencies first.
	overlaid []*types.Package
	// needed holds the packages some checked package imports.
	needed map[*types.Package]bool
	// changed holds the packages whose non-test files the plan edits.
	changed map[*types.Package]bool
}

// orderVerification follows the imports of non-test files from the touched
// packages, and those of every file for the touched packages themselves.
func orderVerification(ws *types.Workspace, touched []*types.Package, changesByFile map[string][]types.Change) verifyOrder {
	byImport := make(map[string]*types.Package, len(ws.Packages))
	for _, p := range ws.Packages {
		byImport[p.ImportPath] = p
	}

	importsOf := func(files []*types.File) []*types.Package {
		var out []*types.Package

		for _, f := range files {
			if f.AST == nil {
				continue
			}

			for _, imp := range f.AST.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err == nil && byImport[path] != nil {
					out = append(out, byImport[path])
				}
			}
		}

		return out
	}
	prodImports := func(p *types.Package) []*types.Package { return importsOf(slices.Collect(maps.Values(p.Files))) }

	o := verifyOrder{needed: make(map[*types.Package]bool), changed: make(map[*types.Package]bool)}
	for _, p := range touched {
		o.changed[p] = changesProduction(p, changesByFile)
	}

	affected := make(map[*types.Package]bool)
	started := make(map[*types.Package]bool)
	var visit func(p *types.Package)

	visit = func(p *types.Package) {
		if started[p] {
			return // done, or an import cycle through test files
		}

		started[p] = true
		for _, d := range prodImports(p) {
			visit(d)

			if o.changed[d] || affected[d] {
				affected[p] = !o.changed[p]
			}
		}

		if o.changed[p] || affected[p] {
			o.overlaid = append(o.overlaid, p)
		}
	}
	for _, p := range touched {
		for _, d := range importsOf(allFilesOf(p)) {
			o.needed[d] = true
			visit(d)
		}

		visit(p)
	}

	for _, p := range o.overlaid {
		for _, d := range prodImports(p) {
			o.needed[d] = true
		}
	}

	return o
}

// verifyTouched checks the touched packages and returns the errors the plan
// adds. Packages that change or import a changed package are checked first
// against the plan's API, and the rest are checked against them. The own
// errors of packages the plan does not edit are not counted.
func verifyTouched(ws *types.Workspace, pkgs []*types.Package, changesByFile map[string][]types.Change, importer gotypes.Importer) (newErrors, skipped []string) {
	ser := NewSerializer()
	over := &overlayImporter{base: importer, over: make(map[string]*gotypes.Package)}
	order := orderVerification(ws, pkgs, changesByFile)

	isTouched := make(map[*types.Package]bool, len(pkgs))
	for _, p := range pkgs {
		isTouched[p] = true
	}

	record := func(res packageVerifyResult) {
		if res.skipped != "" {
			skipped = append(skipped, res.skipped)
			return
		}

		newErrors = append(newErrors, res.newErrors...)
	}

	checked := make(map[*types.Package]bool)

	for _, p := range order.overlaid {
		goVersion := analysis.PackageGoVersion(ws, p.Dir)
		inPackage, _ := packageFileGroups(p)

		production := productionFiles(p, inPackage)
		switch {
		case len(production) == 0:
		case isTouched[p] && len(production) == len(inPackage):
			res := verifyFileGroup(p.ImportPath, goVersion, inPackage, changesByFile, ser, importer, over)
			record(res)

			checked[p] = true
			if res.modified != nil {
				over.over[p.ImportPath] = res.modified
			}
		case order.needed[p]:
			// Importers see the package without its test files.
			res := verifyFileGroup(p.ImportPath, goVersion, production, changesByFile, ser, importer, over)
			if isTouched[p] && res.skipped != "" {
				skipped = append(skipped, res.skipped)
			}

			if res.modified != nil {
				over.over[p.ImportPath] = res.modified
			}
		}
	}

	for _, p := range pkgs {
		goVersion := analysis.PackageGoVersion(ws, p.Dir)
		inPackage, external := packageFileGroups(p)

		if !checked[p] {
			record(verifyFileGroup(p.ImportPath, goVersion, inPackage, changesByFile, ser, importer, over))
		}

		if slices.ContainsFunc(external, func(f *types.File) bool { return changesByFile[f.Path] != nil }) {
			record(verifyFileGroup(p.ImportPath+"_test", goVersion, external, changesByFile, ser, importer, over))
		}
	}

	return newErrors, skipped
}

// productionFiles returns the non-test files among files.
func productionFiles(pkg *types.Package, files []*types.File) []*types.File {
	production := slices.Collect(maps.Values(pkg.Files))
	return slices.DeleteFunc(slices.Clone(files), func(f *types.File) bool { return !slices.Contains(production, f) })
}

// packageFileGroups splits pkg's files into those checked together — the
// non-test files and the test files declaring pkg.Name — and the external
// test files (package x_test), which form a package of their own unless
// there is nothing else to check them with.
func packageFileGroups(pkg *types.Package) (inPackage, external []*types.File) {
	inPackage = slices.Collect(maps.Values(pkg.Files))
	for _, f := range pkg.TestFiles {
		if f.AST.Name.Name == pkg.Name {
			inPackage = append(inPackage, f)
		} else {
			external = append(external, f)
		}
	}

	if len(inPackage) == 0 {
		return external, nil
	}

	return inPackage, external
}

// verifyFileGroup diffs the type errors of files checked as package
// importPath before and after changesByFile is applied. The baseline resolves
// imports through importer and the modified files through modImporter.
func verifyFileGroup(importPath, goVersion string, files []*types.File, changesByFile map[string][]types.Change, ser *Serializer, importer, modImporter gotypes.Importer) packageVerifyResult {
	baselineContents := fileContents(files)
	if len(baselineContents) == 0 {
		return packageVerifyResult{skipped: fmt.Sprintf("package %s: no parseable files", importPath)}
	}

	baseFset := token.NewFileSet()

	baseFiles, err := parseAll(baseFset, baselineContents)
	if err != nil {
		// Should not happen: pkg.Files/TestFiles only ever hold content that
		// already parsed successfully once, at workspace-load time (a
		// package with any unparseable file never makes it into
		// ws.Packages — see GoParser.ParsePackage). A failure
		// re-parsing that same content here would mean something mutated it
		// out from under us, which is exactly the kind of "can't trust the
		// result" situation Skipped exists for.
		return packageVerifyResult{skipped: fmt.Sprintf("package %s: baseline content unexpectedly failed to parse: %v", importPath, err)}
	}

	_, baseErrs := checkPackageErrors(baseFset, importer, importPath, goVersion, baseFiles)
	baseline := normalizeErrorSet(baseErrs)

	modContents, err := applyPlanToFiles(files, changesByFile, ser)
	if err != nil {
		// A plan that does not apply in memory does not apply on disk either.
		return packageVerifyResult{newErrors: []string{fmt.Sprintf("package %s: plan cannot be applied: %v", importPath, err)}}
	}

	if len(modContents) == 0 {
		// The plan deletes every file in the package; there is nothing left
		// to type-check, and that is not itself an error.
		return packageVerifyResult{}
	}

	modFset := token.NewFileSet()

	modFiles, err := parseAll(modFset, modContents)
	if err != nil {
		// The plan's own changes produce invalid Go source. This is exactly
		// the class of bug VerifyPlanTypesWithImporter exists to catch — even more
		// clearly wrong than a type error — so it is reported as a new
		// error rather than as Skipped. Type-checking cannot proceed
		// without a complete, valid file set for the package, so this is
		// the only finding for this package.
		return packageVerifyResult{newErrors: []string{fmt.Sprintf("package %s: plan produces invalid Go source: %v", importPath, err)}}
	}

	modPkg, modErrs := checkPackageErrors(modFset, modImporter, importPath, goVersion, modFiles)

	var newErrs []string

	for _, e := range modErrs {
		key, full := normalizeTypeError(e)
		if !baseline[key] {
			newErrs = append(newErrs, full)
		}
	}

	return packageVerifyResult{newErrors: newErrs, modified: modPkg}
}

// fileContents returns file-path -> original content for files.
func fileContents(files []*types.File) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		out[f.Path] = f.OriginalContent
	}

	return out
}

// applyPlanToFiles returns file-path -> content for files with
// changesByFile's edits applied in memory. A file targeted by a Delete
// change is dropped entirely rather than "edited" — Change.Delete documents
// Start/End/OldText/NewText as meaningless for a delete, and dropping the
// file mirrors what deleteFile actually does on disk, which
// lets verification catch the fallout of a deletion (a sibling file left
// referencing the now-gone symbol) instead of just skipping the file and
// losing that signal.
func applyPlanToFiles(files []*types.File, changesByFile map[string][]types.Change, ser *Serializer) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		changes, ok := changesByFile[f.Path]
		if !ok {
			out[f.Path] = f.OriginalContent
			continue
		}

		if slices.ContainsFunc(changes, func(c types.Change) bool { return c.Delete }) {
			continue // dropped, see doc comment above
		}

		modified, err := ser.ApplyChangesToContent(f.OriginalContent, changes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}

		out[f.Path] = modified
	}

	return out, nil
}

// parseAll parses every entry in contents into an *ast.File registered
// against fset, in a stable (path-sorted) order. All files passed to a
// single go/types.Config.Check call must share one FileSet — mixing files
// from different FileSets produces meaningless positions and, in some
// go/types code paths, a nil-dereference — so callers must always build a
// fresh FileSet per check (see verifyFileGroup: baseFset and modFset are
// never the same instance, and never ws.FileSet).
func parseAll(fset *token.FileSet, contents map[string][]byte) ([]*ast.File, error) {
	paths := make([]string, 0, len(contents))
	for p := range contents {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	files := make([]*ast.File, 0, len(paths))
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, contents[p], parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}

		files = append(files, f)
	}

	return files, nil
}

// checkPackageErrors type-checks files as importPath and returns every error
// go/types produces, not just the first. This is the reason VerifyPlanTypesWithImporter
// cannot reuse (*analysis.GoParser).TypeCheckPackage directly: that method's
// underlying checkFiles sets Config.Error to a no-op (deliberately silently
// ignoring type errors — see its doc comment) and only surfaces the single
// error Config.Check happens to return, whereas a baseline diff needs the
// complete error set on both sides to avoid mistaking "baseline had error A,
// modified has error A and B" for "everything's new".
func checkPackageErrors(fset *token.FileSet, importer gotypes.Importer, importPath, goVersion string, files []*ast.File) (*gotypes.Package, []error) {
	var errs []error
	conf := analysis.NewTypesConfig(importer, goVersion, func(err error) { errs = append(errs, err) })
	info := &gotypes.Info{
		Types: make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:  make(map[*ast.Ident]gotypes.Object),
		Uses:  make(map[*ast.Ident]gotypes.Object),
	}
	// The returned error is redundant with what Error already collected
	// above (Config.Check returns the first error even when a custom Error
	// callback is set), so it is intentionally discarded here.
	pkg, _ := conf.Check(importPath, fset, files, info)

	return pkg, errs
}

// normalizeTypeError reduces a type-checking error to (key, full): full is
// the original, human-readable error text (what ends up in NewErrors); key
// is what the baseline diff actually compares — the error's basename-scoped
// message with its file:line:col location stripped out.
//
// The location must be stripped for the diff to be useful at all: inserting
// or deleting even one line anywhere earlier in a file shifts every
// subsequent line number, so a pre-existing error two lines below the plan's
// edit would otherwise look "new" purely because its line number changed,
// even though nothing about the error itself did. Keeping the basename
// (rather than dropping location entirely) still tells apart two
// identically-worded errors in different files of the same package, which a
// message-only comparison would incorrectly conflate into one.
func normalizeTypeError(err error) (key string, full string) {
	full = err.Error()

	terr, ok := errors.AsType[gotypes.Error](err)
	if ok {
		base := "?"
		if terr.Fset != nil {
			base = filepath.Base(terr.Fset.Position(terr.Pos).Filename)
		}

		return base + ": " + terr.Msg, full
	}
	// Not a go/types.Error (e.g. an *Argument error from a hard failure
	// elsewhere) — fall back to stripping a textual "file:line:col: " prefix
	// if it looks like go/types' usual Error.Error() format, so this rare
	// path degrades to the same comparison instead of never matching.
	idx := strings.Index(full, ": ")
	if idx > 0 {
		prefix := full[:idx]
		if strings.Count(prefix, ":") >= 2 {
			return filepath.Base(prefix) + full[idx:], full
		}
	}

	return full, full
}

// normalizeErrorSet builds the comparison-key set normalizeTypeError feeds
// the baseline diff with.
func normalizeErrorSet(errs []error) map[string]bool {
	set := make(map[string]bool, len(errs))
	for _, e := range errs {
		key, _ := normalizeTypeError(e)
		set[key] = true
	}

	return set
}
