package refactor

import (
	"cmp"
	"fmt"
	gotypes "go/types"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// This file owns the refusal to edit generated code: what discover records when
// the climb touches a generated file, and which hand-written functions the
// error tells the user to stop at.

// generatedHit is one place in a generated file the climb would have to edit.
type generatedHit struct {
	file *pkgtypes.File
	line int
	what string
}

// generatedReach is what discover records about generated code, plus the edges
// that enqueued each function, from which the stops to suggest are derived.
type generatedReach struct {
	hits []generatedHit
	// causes are the functions whose gaining a ctx forces a generated edit.
	causes map[*gotypes.Func]bool
	// callers maps a function to the callers the climb entered through a call.
	callers map[*gotypes.Func][]*gotypes.Func
	// expands maps a function to what interface expansion enqueued for it.
	expands map[*gotypes.Func][]*gotypes.Func
}

func newGeneratedReach() *generatedReach {
	return &generatedReach{
		causes:  make(map[*gotypes.Func]bool),
		callers: make(map[*gotypes.Func][]*gotypes.Func),
		expands: make(map[*gotypes.Func][]*gotypes.Func),
	}
}

// isGaining reports whether f is declared in a generated file and would gain a
// ctx parameter, recording it when so.
func (g *generatedReach) isGaining(f *gotypes.Func, fd *funcDecl) bool {
	if fd.File == nil || !fd.File.Generated || funcHasContextParam(f) {
		return false
	}

	name := fd.FuncType().Pos()
	switch {
	case fd.Decl != nil:
		name = fd.Decl.Name.Pos()
	case fd.IfaceField != nil && len(fd.IfaceField.Names) > 0:
		name = fd.IfaceField.Names[0].Pos()
	}

	g.hit(f, fd.File, offsetLine(fd.File, name), shortLabel(f)+" would gain a ctx parameter")

	return true
}

// isCallSite reports whether a call of f sits in a generated file, recording it
// when so.
func (g *generatedReach) isCallSite(f *gotypes.Func, site analysis.CallSite, line int) bool {
	if site.File == nil || !site.File.Generated {
		return false
	}

	g.hit(f, site.File, line, "call to "+shortLabel(f))

	return true
}

// handWrittenUses records the value uses of f that sit in generated files and
// returns the rest.
func (g *generatedReach) handWrittenUses(ws *pkgtypes.Workspace, f *gotypes.Func, uses []analysis.ValueUse) []analysis.ValueUse {
	var rest []analysis.ValueUse

	for _, use := range uses {
		if use.File == nil || !use.File.Generated {
			rest = append(rest, use)
			continue
		}

		g.hit(f, use.File, ws.FileSet.Position(use.Pos).Line, shortLabel(f)+" used as a value")
	}

	return rest
}

func (g *generatedReach) hit(cause *gotypes.Func, file *pkgtypes.File, line int, what string) {
	g.hits = append(g.hits, generatedHit{file: file, line: line, what: what})
	g.causes[cause] = true
}

func (g *generatedReach) climbed(callee, caller *gotypes.Func) {
	g.callers[callee] = append(g.callers[callee], caller)
}

func (g *generatedReach) expanded(f *gotypes.Func, extra []*gotypes.Func) {
	g.expands[f] = append(g.expands[f], extra...)
}

// crossings returns the functions to stop at so that no cause gains a ctx.
//
// A cause gains through its interface group, which cannot be stopped in part,
// so the stops are the group's members the climb entered by a call, or seeded.
// Those that only another stop leads to are left out.
func (g *generatedReach) crossings(seeds []*gotypes.Func) map[*gotypes.Func]bool {
	entered := make(map[*gotypes.Func]bool)
	for _, f := range seeds {
		entered[f] = true
	}

	for _, callers := range g.callers {
		for _, c := range callers {
			entered[c] = true
		}
	}

	expandedFrom := make(map[*gotypes.Func][]*gotypes.Func)

	for f, extra := range g.expands {
		for _, e := range extra {
			expandedFrom[e] = append(expandedFrom[e], f)
		}
	}

	entries := make(map[*gotypes.Func]bool)

	walk(slices.Collect(maps.Keys(g.causes)), func(f *gotypes.Func) []*gotypes.Func {
		if entered[f] {
			entries[f] = true
		}

		return expandedFrom[f]
	})

	reached := make(map[*gotypes.Func]bool)

	walk(seeds, func(f *gotypes.Func) []*gotypes.Func {
		if entries[f] {
			reached[f] = true
			return nil
		}

		return append(slices.Clone(g.callers[f]), g.expands[f]...)
	})

	return reached
}

// walk visits every function reachable from start through next, once each.
func walk(start []*gotypes.Func, next func(*gotypes.Func) []*gotypes.Func) {
	seen := make(map[*gotypes.Func]bool)

	queue := slices.Clone(start)
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]

		if seen[f] {
			continue
		}

		seen[f] = true
		queue = append(queue, next(f)...)
	}
}

// generatedAbort fails when the climb would have to edit a generated file,
// naming the files and the stop_at entries that keep a re-run out of them.
func (op *ThreadContextOperation) generatedAbort(ws *pkgtypes.Workspace, g *generatedReach, seeds []*gotypes.Func) error {
	if len(g.hits) == 0 {
		return nil
	}

	rel := func(file *pkgtypes.File) string {
		r, err := filepath.Rel(ws.RootPath, file.Path)
		if err == nil {
			return filepath.ToSlash(r)
		}

		return file.Path
	}

	// One example per file: its first hit.
	first := make(map[string]generatedHit)

	for _, h := range g.hits {
		path := rel(h.file)

		prev, ok := first[path]
		if !ok || cmp.Or(cmp.Compare(h.line, prev.line), strings.Compare(h.what, prev.what)) < 0 {
			first[path] = h
		}
	}

	paths := slices.Sorted(maps.Keys(first))

	files := make([]string, 0, len(paths))
	for _, path := range paths {
		files = append(files, fmt.Sprintf("%s (%s:%d: %s)", path, path, first[path].line, first[path].what))
	}

	// A seed is the function that gains the parameter, and a generated function
	// cannot be edited to pass context.TODO(): neither can be a stop.
	var stops, fixed []string

	for f := range g.crossings(seeds) {
		fd := op.declIndex[f]
		if slices.Contains(seeds, f) || fd == nil || fd.File == nil || fd.File.Generated {
			fixed = append(fixed, funcLabel(f))
			continue
		}

		stops = append(stops, fmt.Sprintf("%q", rel(fd.File)+":"+shortLabel(f)))
	}

	slices.Sort(stops)
	slices.Sort(fixed)

	var advice string

	switch {
	case len(fixed) == 1:
		advice = fmt.Sprintf("No stop_at can avoid this: %s is the function that gains the parameter; "+
			"change the generator instead.", fixed[0])
	case len(fixed) > 1:
		advice = fmt.Sprintf("No stop_at can avoid this: %s are the functions that gain the parameter; "+
			"change the generator instead.", strings.Join(fixed, ", "))
	default:
		advice = fmt.Sprintf("To keep the climb out of generated code, pass stop_at: [%s].", strings.Join(stops, ", "))
	}

	seed := cmp.Or(op.FunctionName, "the call sites of "+strings.Join(op.CallSitesOf, ", "))
	if op.seed != nil {
		seed = funcLabel(op.seed)
	}

	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf("cannot thread context through %s: the climb reaches generated code, which its "+
			"generator would overwrite: %s. %s No changes were made.", seed, strings.Join(files, "; "), advice),
		File: first[paths[0]].file.Path,
		Line: first[paths[0]].line,
	}
}

// shortLabel renders f as "Type.Method" or "Name", the form stop_at accepts.
func shortLabel(f *gotypes.Func) string {
	key, ok := pkgtypes.SymbolKeyOf(f)
	if !ok {
		return funcLabel(f)
	}

	return strings.TrimPrefix(key.Recv+"."+key.Name, ".")
}
