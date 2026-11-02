package refactor

import (
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// packageAllFiles returns every file in pkg — both regular source files and
// _test.go files, which the parser keeps in separate maps. Move operations
// must treat them uniformly so tests are relocated and rewritten alongside
// the code they cover.
func packageAllFiles(pkg *types.Package) []*types.File {
	all := make([]*types.File, 0, len(pkg.Files)+len(pkg.TestFiles))
	for _, f := range pkg.Files {
		all = append(all, f)
	}
	for _, f := range pkg.TestFiles {
		all = append(all, f)
	}
	return all
}

// sourceFilesOf returns every .go file in dir whatever its build constraints,
// refusing a directory holding a file that could not be read.
func sourceFilesOf(tree *analysis.SourceTree, dir string) ([]*analysis.SourceFile, error) {
	files := tree.Dir(dir)
	for _, f := range files {
		if f.Content == nil && f.Err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", f.Path, f.Err)
		}
	}
	return files, nil
}

// findSourceDir resolves source to a source-tree directory holding Go files.
// source may be an absolute directory, one relative to the workspace root, or
// an import path; failing those, a path-suffix of exactly one directory. Several
// suffix matches are an error listing them; ok is false when nothing matches.
func findSourceDir(ws *types.Workspace, tree *analysis.SourceTree, source string) (dir string, ok bool, err error) {
	var suffixed []string
	for _, d := range tree.Dirs() {
		if len(tree.Dir(d)) == 0 {
			continue
		}
		if d == source || d == filepath.Join(ws.RootPath, source) || packagePathToImportPath(ws, d) == source {
			return d, true, nil
		}
		if strings.HasSuffix(d, string(filepath.Separator)+source) {
			suffixed = append(suffixed, d)
		}
	}
	switch len(suffixed) {
	case 0:
		return "", false, nil
	case 1:
		return suffixed[0], true, nil
	}
	return "", false, fmt.Errorf("package %q is ambiguous: it matches %s; use a path relative to the workspace root or an import path",
		source, strings.Join(suffixed, ", "))
}

// appendContentRewrites adds a whole-file change for every source-tree file
// that skip does not exclude and that rewrite alters.
func appendContentRewrites(plan *types.RefactoringPlan, tree *analysis.SourceTree, skip func(*analysis.SourceFile) bool,
	rewrite func(*analysis.SourceFile) (string, error), description string) error {
	for _, dir := range tree.Dirs() {
		for _, file := range tree.Dir(dir) {
			if skip(file) || len(file.Content) == 0 {
				continue
			}
			content := string(file.Content)
			newContent, err := rewrite(file)
			if err != nil {
				return err
			}
			if newContent == content {
				continue
			}
			plan.Changes = append(plan.Changes, types.Change{
				File:        file.Path,
				Start:       0,
				End:         len(file.Content),
				OldText:     content,
				NewText:     newContent,
				Description: description,
			})
			if !contains(plan.AffectedFiles, file.Path) {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}
	return nil
}

// MovePackageOperation implements moving entire packages
type MovePackageOperation struct {
	Request types.MovePackageRequest
	// Sources holds every .go file of the workspace, so files excluded by the
	// loaded build configuration are moved and rewritten too.
	Sources *analysis.SourceTree
	// Views are the build configurations that type-check the importers.
	Views []*analysis.View
}

func (op *MovePackageOperation) Type() types.OperationType {
	return types.MovePackageOperation
}

func (op *MovePackageOperation) Description() string {
	return fmt.Sprintf("Move package %s to %s", op.Request.SourcePackage, op.Request.TargetPackage)
}

func (op *MovePackageOperation) Validate(ws *types.Workspace) error {
	_, err := resolvePackageMove(ws, op.Sources, op.Request.SourcePackage, op.Request.TargetPackage)
	return err
}

// dirMove is one package relocation between absolute directories.
type dirMove struct{ source, target string }

// resolvePackageMove resolves a source package and target path to directories.
// The target is refused when it already holds Go files in the source tree, so
// two packages are never merged.
func resolvePackageMove(ws *types.Workspace, sources *analysis.SourceTree, source, target string) (dirMove, error) {
	if source == "" {
		return dirMove{}, fmt.Errorf("source package cannot be empty")
	}
	if target == "" {
		return dirMove{}, fmt.Errorf("target package cannot be empty")
	}
	sourceDir, ok, err := findSourceDir(ws, sources, source)
	if err != nil {
		return dirMove{}, err
	}
	if !ok {
		return dirMove{}, fmt.Errorf("source package %s not found in workspace", source)
	}
	// Request.TargetPackage may be an absolute path, a module import path, or a
	// workspace-relative path; everything downstream needs the absolute form.
	targetDir, err := types.ResolveNewPackagePath(ws, target)
	if err != nil {
		return dirMove{}, fmt.Errorf("resolving target package %q: %w", target, err)
	}
	if packagePathToImportPath(ws, targetDir) == "" {
		return dirMove{}, fmt.Errorf("cannot determine the import path for target package %s", targetDir)
	}
	// Moving onto a package that already exists would merge two packages: files
	// landing alongside each other, same-named ones silently overwritten, and
	// duplicate declarations undetected until the build. Refuse and let the
	// caller pick a target or do the merge deliberately.
	if existing := sources.Dir(targetDir); len(existing) > 0 {
		return dirMove{}, fmt.Errorf("target package %s already exists at %s (package %q); "+
			"move_package will not merge into an existing package — choose a target that does not exist yet",
			target, targetDir, sourcePackageName(ws, targetDir, existing))
	}
	return dirMove{source: sourceDir, target: targetDir}, nil
}

// markRelocation flags every change of plan as part of a whole-file or package relocation.
func markRelocation(plan *types.RefactoringPlan) *types.RefactoringPlan {
	for i := range plan.Changes {
		plan.Changes[i].Relocation = true
	}
	return plan
}

func (op *MovePackageOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	move, err := resolvePackageMove(ws, op.Sources, op.Request.SourcePackage, op.Request.TargetPackage)
	if err != nil {
		return nil, err
	}
	carried, err := carriedByPackages([]dirMove{move})
	if err != nil {
		return nil, err
	}
	return planDirMoves(op, ws, op.Sources, op.Views, []dirMove{move}, carried,
		fmt.Sprintf("Update references after moving %s to %s", op.Request.SourcePackage, op.Request.TargetPackage))
}

// MoveDirOperation implements moving directory structures
type MoveDirOperation struct {
	Request types.MoveDirRequest
	Sources *analysis.SourceTree
	Views   []*analysis.View
}

func (op *MoveDirOperation) Type() types.OperationType {
	return types.MoveDirOperation
}

func (op *MoveDirOperation) Description() string {
	return fmt.Sprintf("Move directory %s to %s", op.Request.SourceDir, op.Request.TargetDir)
}

func (op *MoveDirOperation) Validate(ws *types.Workspace) error {
	if op.Request.SourceDir == "" {
		return fmt.Errorf("source directory cannot be empty")
	}
	if op.Request.TargetDir == "" {
		return fmt.Errorf("target directory cannot be empty")
	}

	// Check if source directory exists in workspace
	sourcePath := filepath.Join(ws.RootPath, op.Request.SourceDir)
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		return fmt.Errorf("source directory %s does not exist", sourcePath)
	}

	return nil
}

// dirPkgRewrite captures everything the move induces for one relocated
// package: its old→new import path, and — when the target directory's base
// name differs — its old→new package identifier.
type dirPkgRewrite struct {
	oldImportPath string
	newImportPath string
	oldName       string
	newName       string
	renamed       bool
}

func (op *MoveDirOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	sourceDirPath := filepath.Join(ws.RootPath, op.Request.SourceDir)
	targetDirPath := filepath.Join(ws.RootPath, op.Request.TargetDir)

	// Every directory within the source dir that holds Go files, whatever
	// their build constraints. The boundary-aware prefix check keeps
	// "internal/foo" from also matching a sibling "internal/foobar".
	var moves []dirMove
	for _, dir := range op.Sources.Dirs() {
		if (dir == sourceDirPath || strings.HasPrefix(dir, sourceDirPath+string(filepath.Separator))) && len(op.Sources.Dir(dir)) > 0 {
			rel, err := filepath.Rel(sourceDirPath, dir)
			if err != nil {
				return nil, fmt.Errorf("computing target path for package %s: %w", dir, err)
			}
			moves = append(moves, dirMove{source: dir, target: filepath.Join(targetDirPath, rel)})
		}
	}
	if len(moves) == 0 {
		return nil, fmt.Errorf("no packages found in source directory %s", op.Request.SourceDir)
	}
	carried, err := carriedByDir(dirMove{source: sourceDirPath, target: targetDirPath}, moves)
	if err != nil {
		return nil, err
	}
	return planDirMoves(op, ws, op.Sources, op.Views, moves, carried,
		fmt.Sprintf("Update references after moving %s to %s", op.Request.SourceDir, op.Request.TargetDir))
}

// planDirMoves plans every move together: each touched file becomes exactly one
// whole-file change covering all moved packages' import paths and renamed
// qualifiers, and moved files are excluded from the importer pass.
// carried are the non-package files relocated alongside, which keep their bytes
// and mode. refsDescription labels the importer rewrites.
func planDirMoves(op types.Operation, ws *types.Workspace, sources *analysis.SourceTree, views []*analysis.View,
	moves []dirMove, carried []fileMove, refsDescription string) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Build the rewrite for each moved package and collect its files.
	rewrites := make([]dirPkgRewrite, 0, len(moves))
	rewriteByDir := make(map[string]dirPkgRewrite, len(moves))
	filesByDir := make(map[string][]*analysis.SourceFile, len(moves))

	for _, m := range moves {
		files, err := sourceFilesOf(sources, m.source)
		if err != nil {
			return nil, err
		}
		filesByDir[m.source] = files
		newName := filepath.Base(m.target)
		oldName := sourcePackageName(ws, m.source, files)
		rw := dirPkgRewrite{
			oldImportPath: packagePathToImportPath(ws, m.source),
			newImportPath: packagePathToImportPath(ws, m.target),
			oldName:       oldName,
			newName:       newName,
			// Only rename when the directory's base name changed AND is a
			// legal Go identifier; otherwise keep the original package name.
			// A main package keeps its name, so its external tests keep main.X.
			renamed: newName != oldName && oldName != "main" && isValidGoIdentifier(newName),
		}
		rewrites = append(rewrites, rw)
		rewriteByDir[m.source] = rw
	}
	var renames []pkgRename
	for _, rw := range rewrites {
		if rw.renamed && rw.oldImportPath != "" {
			renames = append(renames, pkgRename{oldPath: rw.oldImportPath, oldName: rw.oldName, newName: rw.newName})
		}
	}

	rewriteImportPaths := func(content string) string {
		for _, rw := range rewrites {
			if rw.oldImportPath != "" && rw.newImportPath != "" && rw.oldImportPath != rw.newImportPath {
				content = strings.ReplaceAll(content, `"`+rw.oldImportPath+`"`, `"`+rw.newImportPath+`"`)
			}
		}
		return content
	}
	// rewriteContent applies every renamed package's qualifier rewrite, then
	// every moved package's import-path rewrite, so each touched file becomes
	// exactly one whole-file change and the serializer sees no overlap. at is
	// the path the file ends up at; clause carries its own package clause edit, if any.
	rewriteContent := func(file *analysis.SourceFile, at string, clause ...textEdit) (string, error) {
		content, skipped, err := qualifierRewrite(views, ws.FileSet, file, renames, clause...)
		if err != nil {
			return "", err
		}
		for _, site := range skipped {
			site.File = at
			plan.SkippedSites = append(plan.SkippedSites, site)
		}
		return rewriteImportPaths(content), nil
	}

	// Move every file (source + test) of each moved package. Each file is
	// rewritten once and its original deleted.
	for _, m := range moves {
		self := rewriteByDir[m.source]
		for _, file := range filesByDir[m.source] {
			if len(file.Content) == 0 {
				continue
			}
			var clause []textEdit
			if self.renamed {
				start, end, replacement, ok, err := packageClauseEdit(ws.FileSet, file, self.oldName, self.newName)
				if err != nil {
					return nil, err
				}
				if ok {
					clause = append(clause, textEdit{start: start, end: end, text: replacement})
				}
			}
			targetFilePath := filepath.Join(m.target, file.Name)
			content, err := rewriteContent(file, targetFilePath, clause...)
			if err != nil {
				return nil, err
			}

			appendFileMove(plan, file.Path, targetFilePath, content, 0)
		}
	}

	// A carried Go file (under testdata) is not part of any package, so only its
	// import paths are re-pointed.
	carriedSources := make(map[string]bool, len(carried))
	for _, c := range carried {
		carriedSources[c.source] = true
		content, err := os.ReadFile(c.source)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", c.source, err)
		}
		text := string(content)
		if filepath.Ext(c.source) == ".go" {
			text = rewriteImportPaths(text)
		}
		appendFileMove(plan, c.source, c.target, text, c.mode)
	}

	// Rewrite imports (and renamed-package references) in every other source
	// file that is not itself being moved.
	err := appendContentRewrites(plan, sources,
		func(f *analysis.SourceFile) bool {
			_, moved := rewriteByDir[f.Dir]
			return moved || carriedSources[f.Path]
		},
		func(f *analysis.SourceFile) (string, error) { return rewriteContent(f, f.Path) },
		refsDescription)
	if err != nil {
		return nil, err
	}

	return markRelocation(plan), nil
}

// appendFileMove adds the create and delete changes that move one file.
func appendFileMove(plan *types.RefactoringPlan, from, to, content string, mode os.FileMode) {
	plan.Changes = append(plan.Changes,
		types.Change{
			File:        to,
			NewText:     content,
			Mode:        mode,
			Description: fmt.Sprintf("Move file from %s to %s", from, to),
		},
		types.Change{
			File:        from,
			Delete:      true,
			Description: fmt.Sprintf("Remove file %s (moved to %s)", from, to),
		})
	plan.AffectedFiles = append(plan.AffectedFiles, from, to)
}

// sourcePackageName returns the package name of dir: the loaded package's, or
// for a directory no loaded configuration includes, the first non-main clause
// among its files (falling back to the first clause of any kind).
func sourcePackageName(ws *types.Workspace, dir string, files []*analysis.SourceFile) string {
	if pkg := ws.Packages[dir]; pkg != nil {
		return pkg.Name
	}
	name := ""
	for _, f := range files {
		if f.AST == nil || f.AST.Name == nil || f.AST.Name.Name == "" {
			continue
		}
		clause := strings.TrimSuffix(f.AST.Name.Name, "_test")
		if clause != "main" {
			return clause
		}
		if name == "" {
			name = clause
		}
	}
	return name
}

// packageClauseEdit locates the package clause identifier of file and the name
// it must take when oldName becomes newName, via the file's AST so a stray
// "package oldName" inside a doc comment can't be hit. An external test package
// (oldName_test) becomes newName_test; a file with any other clause (an
// ignore-tagged package main, say) needs no edit (ok is false). It fails when
// the clause must change but the file has no usable AST.
func packageClauseEdit(fset *token.FileSet, file *analysis.SourceFile, oldName, newName string) (start, end int, replacement string, ok bool, err error) {
	if oldName == newName || oldName == "main" {
		return 0, 0, "", false, nil
	}
	if file.AST == nil || file.AST.Name == nil || file.AST.Name.Name == "" {
		return 0, 0, "", false, fmt.Errorf("cannot rewrite the package clause of %s: file does not parse: %v", file.Path, file.Err)
	}
	switch file.AST.Name.Name {
	case oldName:
		replacement = newName
	case oldName + "_test":
		replacement = newName + "_test"
	default:
		return 0, 0, "", false, nil
	}
	start = fset.Position(file.AST.Name.Pos()).Offset
	end = fset.Position(file.AST.Name.End()).Offset
	return start, end, replacement, true, nil
}

// MovePackagesOperation implements moving multiple packages atomically
type MovePackagesOperation struct {
	Request types.MovePackagesRequest
	Sources *analysis.SourceTree
	Views   []*analysis.View
}

func (op *MovePackagesOperation) Type() types.OperationType {
	return types.MovePackagesOperation
}

func (op *MovePackagesOperation) Description() string {
	return fmt.Sprintf("Move %d packages", len(op.Request.Packages))
}

func (op *MovePackagesOperation) Validate(ws *types.Workspace) error {
	_, err := op.moves(ws)
	return err
}

// moves resolves every mapping, refusing two that share a source or a target
// directory.
func (op *MovePackagesOperation) moves(ws *types.Workspace) ([]dirMove, error) {
	if len(op.Request.Packages) == 0 {
		return nil, fmt.Errorf("no packages specified for move operation")
	}
	moves := make([]dirMove, 0, len(op.Request.Packages))
	bySource := make(map[string]types.PackageMapping)
	byTarget := make(map[string]types.PackageMapping)
	for i, mapping := range op.Request.Packages {
		if mapping.SourcePackage == "" {
			return nil, fmt.Errorf("source package at index %d cannot be empty", i)
		}
		if mapping.TargetPackage == "" {
			return nil, fmt.Errorf("target package at index %d cannot be empty", i)
		}
		move, err := resolvePackageMove(ws, op.Sources, mapping.SourcePackage, mapping.TargetPackage)
		if err != nil {
			return nil, fmt.Errorf("moving %s to %s: %w", mapping.SourcePackage, mapping.TargetPackage, err)
		}
		if prev, dup := bySource[move.source]; dup {
			return nil, fmt.Errorf("mappings %s -> %s and %s -> %s share the source directory %s",
				prev.SourcePackage, prev.TargetPackage, mapping.SourcePackage, mapping.TargetPackage, move.source)
		}
		if prev, dup := byTarget[move.target]; dup {
			return nil, fmt.Errorf("mappings %s -> %s and %s -> %s share the target directory %s",
				prev.SourcePackage, prev.TargetPackage, mapping.SourcePackage, mapping.TargetPackage, move.target)
		}
		bySource[move.source] = mapping
		byTarget[move.target] = mapping
		moves = append(moves, move)
	}
	return moves, nil
}

func (op *MovePackagesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	moves, err := op.moves(ws)
	if err != nil {
		return nil, err
	}
	carried, err := carriedByPackages(moves)
	if err != nil {
		return nil, err
	}
	return planDirMoves(op, ws, op.Sources, op.Views, moves, carried,
		fmt.Sprintf("Update references after moving %d packages", len(moves)))
}

// CreateFacadeOperation implements creating facade packages
type CreateFacadeOperation struct {
	Request types.CreateFacadeRequest
}

func (op *CreateFacadeOperation) Type() types.OperationType {
	return types.CreateFacadeOperation
}

func (op *CreateFacadeOperation) Description() string {
	return fmt.Sprintf("Create facade package %s with %d exports", op.Request.TargetPackage, len(op.Request.Exports))
}

func (op *CreateFacadeOperation) Validate(ws *types.Workspace) error {
	if op.Request.TargetPackage == "" {
		return fmt.Errorf("target package cannot be empty")
	}
	if len(op.Request.Exports) == 0 {
		return fmt.Errorf("no exports specified for facade")
	}

	// Validate each export spec
	for i, export := range op.Request.Exports {
		if export.SourcePackage == "" {
			return fmt.Errorf("source package at export index %d cannot be empty", i)
		}
		if export.SymbolName == "" {
			return fmt.Errorf("symbol name at export index %d cannot be empty", i)
		}
	}

	return nil
}

func (op *CreateFacadeOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// A facade package is new by definition, so its target can never resolve
	// against the existing package set; it has to be resolved as a path.
	facadeDir, err := types.ResolveNewPackagePath(ws, op.Request.TargetPackage)
	if err != nil {
		return nil, fmt.Errorf("resolving facade target package %q: %w", op.Request.TargetPackage, err)
	}
	facadePkgName := filepath.Base(facadeDir)

	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Generate facade package content
	var facadeContent strings.Builder
	fmt.Fprintf(&facadeContent, "// Package %s provides a facade for accessing related functionality.\n", facadePkgName)
	facadeContent.WriteString("// This file was generated by gorefactor.\n")
	fmt.Fprintf(&facadeContent, "package %s\n\n", facadePkgName)

	// Collect unique source package imports
	imports := make(map[string]bool)
	for _, export := range op.Request.Exports {
		imports[export.SourcePackage] = true
	}
	if len(imports) > 0 {
		facadeContent.WriteString("import (\n")
		for imp := range imports {
			fmt.Fprintf(&facadeContent, "\t\"%s\"\n", imp)
		}
		facadeContent.WriteString(")\n\n")
	}

	// Add kind-appropriate re-export lines
	for _, export := range op.Request.Exports {
		outputName := export.Alias
		if outputName == "" {
			outputName = export.SymbolName
		}
		pkgAlias := filepath.Base(export.SourcePackage)
		kind := lookupSymbolKind(ws, export.SourcePackage, export.SymbolName)

		fmt.Fprintf(&facadeContent, "// %s is re-exported from %s\n", outputName, export.SourcePackage)
		switch kind {
		case types.FunctionSymbol, types.VariableSymbol:
			fmt.Fprintf(&facadeContent, "var %s = %s.%s\n\n", outputName, pkgAlias, export.SymbolName)
		case types.ConstantSymbol:
			fmt.Fprintf(&facadeContent, "const %s = %s.%s\n\n", outputName, pkgAlias, export.SymbolName)
		default: // TypeSymbol, InterfaceSymbol, or unknown → type alias
			fmt.Fprintf(&facadeContent, "type %s = %s.%s\n\n", outputName, pkgAlias, export.SymbolName)
		}
	}

	facadeFile := filepath.Join(facadeDir, "facade.go")
	plan.Changes = append(plan.Changes, types.Change{
		File:        facadeFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     facadeContent.String(),
		Description: fmt.Sprintf("Create facade package %s", op.Request.TargetPackage),
	})

	plan.AffectedFiles = []string{facadeFile}

	return plan, nil
}

// lookupSymbolKind returns the SymbolKind for a named symbol within a source package
// identified by its Go import path. Returns TypeSymbol as a fallback.
func lookupSymbolKind(ws *types.Workspace, sourcePackageImportPath, symbolName string) types.SymbolKind {
	fsPath, ok := ws.ImportToPath[sourcePackageImportPath]
	if !ok {
		return types.TypeSymbol
	}
	pkg, ok := ws.Packages[fsPath]
	if !ok || pkg.Symbols == nil {
		return types.TypeSymbol
	}
	sym := pkg.Symbols.FindSymbol(symbolName)
	if sym == nil {
		return types.TypeSymbol
	}
	return sym.Kind
}
