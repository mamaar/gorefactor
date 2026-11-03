package analysis

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"sync"

	"golang.org/x/tools/go/packages"
)

// externalSet holds the packages a load listed for one build configuration.
// Each is type-checked from source, without function bodies, when first imported.
type externalSet struct {
	fset  *token.FileSet
	sizes gotypes.Sizes // of the configuration's architecture, as go/packages checks with

	// mu serializes type-checking; views that fell back to another view's set
	// import from it concurrently.
	mu     sync.Mutex
	byPath map[string]*externalPackage
}

// externalPackage is one listed package and, once checked, its types.
type externalPackage struct {
	meta    *packages.Package
	imports map[string]*externalPackage
	files   []*ast.File // parsed and not yet checked

	types    *gotypes.Package
	checking bool
	checked  bool
}

// newExternalSet indexes roots and everything they import by import path. Of
// several listed variants of one path, the one compiled for no test is kept.
func newExternalSet(fset *token.FileSet, goarch string, roots []*packages.Package) *externalSet {
	s := &externalSet{fset: fset, sizes: gotypes.SizesFor("gc", goarch), byPath: make(map[string]*externalPackage)}
	byID := make(map[string]*externalPackage)
	for _, root := range roots {
		s.add(byID, root)
	}
	return s
}

func (s *externalSet) add(byID map[string]*externalPackage, meta *packages.Package) *externalPackage {
	if meta == nil {
		return nil
	}
	n := byID[meta.ID]
	if n != nil {
		return n
	}
	n = &externalPackage{meta: meta, imports: make(map[string]*externalPackage, len(meta.Imports))}
	byID[meta.ID] = n
	prev := s.byPath[meta.PkgPath]
	if prev == nil || meta.ID == meta.PkgPath {
		s.byPath[meta.PkgPath] = n
	}
	for path, dep := range meta.Imports {
		n.imports[path] = s.add(byID, dep)
	}
	return n
}

// lookup returns the types of the package listed for path, or nil when the
// load listed none. A package the load listed but could not read is an error.
func (s *externalSet) lookup(path string) (*gotypes.Package, error) {
	if s == nil {
		return nil, nil
	}
	n := s.byPath[path]
	if n == nil {
		return nil, nil
	}
	err := n.listingError()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !n.checked {
		s.parse(n)
		s.check(n)
	}
	return n.types, nil
}

// listingError returns why the load could not read n, which it then lists
// without a package name: its module is not in the module cache, the path
// names no package, or build constraints exclude every file.
func (n *externalPackage) listingError() error {
	if n.meta.Types != nil || n.meta.Name != "" {
		return nil
	}
	if len(n.meta.Errors) > 0 {
		return errors.New(n.meta.Errors[0].Msg)
	}
	return fmt.Errorf("the package listing has no name and no files for %s", n.meta.PkgPath)
}

// parse parses the files of n and of every unchecked package it reaches.
func (s *externalSet) parse(n *externalPackage) {
	p := &externalParser{fset: s.fset, seen: make(map[*externalPackage]bool)}
	p.collect(n)

	next := make(chan parseJob, len(p.jobs))
	for _, job := range p.jobs {
		next <- job
	}
	close(next)
	var wg sync.WaitGroup
	for range workerCount(len(p.jobs)) {
		wg.Add(1)
		go p.work(next, &wg)
	}
	wg.Wait()
}

// parseJob is one file to parse into its slot of a package's files.
type parseJob struct {
	pkg  *externalPackage
	slot int
}

type externalParser struct {
	fset *token.FileSet
	seen map[*externalPackage]bool
	jobs []parseJob
}

func (p *externalParser) collect(n *externalPackage) {
	if n == nil || n.checked || p.seen[n] {
		return
	}
	p.seen[n] = true
	if n.meta.Types == nil && n.files == nil {
		n.files = make([]*ast.File, len(n.meta.CompiledGoFiles))
		for i := range n.files {
			p.jobs = append(p.jobs, parseJob{pkg: n, slot: i})
		}
	}
	for _, dep := range n.imports {
		p.collect(dep)
	}
}

func (p *externalParser) work(next chan parseJob, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range next {
		name := job.pkg.meta.CompiledGoFiles[job.slot]
		// A file with errors still yields the declarations that parsed.
		job.pkg.files[job.slot], _ = parser.ParseFile(p.fset, name, nil, parser.AllErrors|parser.SkipObjectResolution)
	}
}

// check type-checks n after the packages it imports. s.mu must be held and n
// parsed.
func (s *externalSet) check(n *externalPackage) {
	if n.checked || n.checking {
		return
	}
	n.checking = true
	defer s.finish(n)

	switch {
	case n.meta.Types != nil:
		// The load already carries types.
		if n.meta.Types.Complete() {
			n.types = n.meta.Types
		}
		return
	case n.meta.PkgPath == "unsafe":
		n.types = gotypes.Unsafe
		return
	}

	var files []*ast.File
	for _, f := range n.files {
		if f != nil {
			files = append(files, f)
		}
	}
	n.types = gotypes.NewPackage(n.meta.PkgPath, n.meta.Name)
	conf := gotypes.Config{
		Importer:         &externalImporter{set: s, pkg: n},
		IgnoreFuncBodies: true,
		Error:            ignoreTypeError,
		Sizes:            s.sizes,
	}
	_ = gotypes.NewChecker(&conf, s.fset, n.types, nil).Files(files)
}

func (s *externalSet) finish(n *externalPackage) {
	n.files = nil
	n.checking = false
	n.checked = true
}

// ignoreTypeError keeps go/types checking past the first error.
func ignoreTypeError(error) {}

// externalImporter resolves the imports of one external package as the load
// listed them.
type externalImporter struct {
	set *externalSet
	pkg *externalPackage
}

func (i *externalImporter) Import(path string) (*gotypes.Package, error) {
	if path == "unsafe" {
		return gotypes.Unsafe, nil
	}
	dep := i.pkg.imports[path]
	if dep == nil {
		return nil, fmt.Errorf("no metadata for %s", path)
	}
	err := dep.listingError()
	if err != nil {
		return nil, err
	}
	i.set.check(dep)
	if dep.types == nil {
		return nil, fmt.Errorf("no types for %s", path)
	}
	return dep.types, nil
}
