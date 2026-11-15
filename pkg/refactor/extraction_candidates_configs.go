package refactor

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
)

// ExtractionCandidatesRequest selects what ExtractionCandidatesAcrossConfigs reports.
type ExtractionCandidatesRequest struct {
	// Packages are the import paths or directories naming the package.
	Packages []string
	// MaxDependencies keeps the candidates dragging along at most this many
	// declarations; nil keeps every candidate.
	MaxDependencies *int
}

// ExtractionCandidate is a declaration that can leave its package together
// with the declarations it depends on.
type ExtractionCandidate struct {
	// Symbols are the names to move together, sorted.
	Symbols []string
	// DependsOn is Symbols without the candidate's own names.
	DependsOn []string
	Tier      int // len(DependsOn)
	Lines     int
	// UsedBy counts the declarations that stay and reference the moved set.
	UsedBy int
	// NeedsExport names the unexported members of the set that code staying
	// behind, tests included, references.
	NeedsExport []string
	File        string
	Line        int
	// Configs names the configurations that declare it; empty when all do.
	Configs []string
	// Blockers are the reasons move_symbol would refuse to move Symbols, in
	// move_symbol's wording.
	Blockers []string
}

// ExtractionCandidatesReport is the outcome of ExtractionCandidatesAcrossConfigs.
type ExtractionCandidatesReport struct {
	DetectHeader
	Candidates []ExtractionCandidate
}

// ExtractionCandidatesAcrossConfigs lists the top-level declarations of one
// package with what each drags along when moved, lowest-hanging first. The
// dependencies are the union over build configurations.
func (e *DefaultEngine) ExtractionCandidatesAcrossConfigs(req ExtractionCandidatesRequest) (*ExtractionCandidatesReport, error) {
	run := &extractionRun{detectRun: newDetectRun(req.Packages), units: make(map[declSite]*mergedUnit), blockers: make(map[declSite]map[string]bool), primaryDecls: make(map[declSite]bool), primaryRefs: make(map[primaryRef]bool)}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("extraction candidates across configs: %w", err)
	}
	defer e.endRun(run)

	for _, v := range views {
		err := run.visit(v)
		if err != nil {
			return nil, viewError(views, v, err)
		}
	}

	return run.report(req, views), nil
}

// mergedUnit is an analysis.ExtractionUnit as every configuration sees it.
type mergedUnit struct {
	site      declSite
	line      int
	candidate bool
	names     map[string]bool
	decls     map[declSite]analysis.ExtractionDecl
	refs      map[analysis.ExtractionRef]bool
	testRefs  map[string]bool
	configs   []string
}

// primaryRef is a reference between units the default configuration makes.
type primaryRef struct {
	from declSite
	ref  analysis.ExtractionRef
}

type extractionRun struct {
	detectRun
	units map[declSite]*mergedUnit
	// blockers holds, per candidate, what move_symbol refuses in a
	// configuration that holds the candidate's set.
	blockers map[declSite]map[string]bool
	// primaryDecls and primaryRefs are what the default configuration holds.
	primaryDecls map[declSite]bool
	primaryRefs  map[primaryRef]bool
}

func (r *extractionRun) visit(v *analysis.View) error {
	idx := r.indexOf(v)
	if !idx.HasTypeInfo() {
		return fmt.Errorf("no package has type information")
	}

	primary := v == r.views[0]

	for _, pkg := range v.Workspace.Packages {
		if !r.wanted(pkg) {
			continue
		}

		local := make(map[declSite]*mergedUnit)
		raw := make(map[declSite]analysis.ExtractionUnit)

		for _, u := range analysis.ExtractionUnits(v.Workspace, v.Resolver, idx, pkg, wholeGroup) {
			raw[u.Site] = u
			mergeUnit(local, u, v.Config.String())
			mergeUnit(r.units, u, v.Config.String())

			if primary {
				r.notePrimary(u)
			}
		}

		r.collectBlockers(v.Workspace, pkg, raw, local)
	}

	r.collectTypeErrors(v)
	r.planned(v, nil)

	return nil
}

func (r *extractionRun) notePrimary(u analysis.ExtractionUnit) {
	for _, d := range u.Decls {
		r.primaryDecls[d.Site] = true
	}

	for _, ref := range u.Refs {
		r.primaryRefs[primaryRef{u.Site, ref}] = true
	}
}

// mergeUnit folds u, as seen under config, into units.
func mergeUnit(units map[declSite]*mergedUnit, u analysis.ExtractionUnit, config string) {
	m := units[u.Site]
	if m == nil {
		m = &mergedUnit{
			site: u.Site, line: u.Line,
			names: map[string]bool{}, decls: map[declSite]analysis.ExtractionDecl{},
			refs: map[analysis.ExtractionRef]bool{}, testRefs: map[string]bool{},
		}
		units[u.Site] = m
	}

	m.candidate = m.candidate || u.Candidate
	m.configs = append(m.configs, config)

	for _, n := range u.Names {
		m.names[n] = true
	}

	for _, d := range u.Decls {
		m.decls[d.Site] = d
	}

	for _, ref := range u.Refs {
		m.refs[ref] = true
	}

	for _, n := range u.TestRefs {
		m.testRefs[n] = true
	}
}

// closureOf returns the sites of the units head references, transitively, and head.
func closureOf(units map[declSite]*mergedUnit, head declSite) map[declSite]bool {
	set := map[declSite]bool{head: true}

	for todo := []*mergedUnit{units[head]}; len(todo) > 0; {
		cur := todo[0]
		todo = todo[1:]

		for ref := range cur.refs {
			next := units[ref.To]
			if next != nil && !set[ref.To] {
				set[ref.To] = true

				todo = append(todo, next)
			}
		}
	}

	return set
}

func (r *extractionRun) report(req ExtractionCandidatesRequest, views []*analysis.View) *ExtractionCandidatesReport {
	out := &ExtractionCandidatesReport{DetectHeader: r.header(views)}

	for _, m := range r.units {
		if !m.candidate {
			continue
		}

		c := r.candidate(m, len(views) > 1)
		if len(c.Configs) == len(views) {
			c.Configs = nil
		}

		if req.MaxDependencies == nil || c.Tier <= *req.MaxDependencies {
			out.Candidates = append(out.Candidates, c)
		}
	}

	slices.SortFunc(out.Candidates, func(a, b ExtractionCandidate) int {
		return cmp.Or(
			cmp.Compare(a.Tier, b.Tier),
			cmp.Compare(blockedRank(a), blockedRank(b)),
			cmp.Compare(b.Lines, a.Lines),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			slices.Compare(a.Symbols, b.Symbols),
		)
	})

	return out
}

// candidate computes what moving m drags along: the units it references,
// transitively.
func (r *extractionRun) candidate(m *mergedUnit, multiConfig bool) ExtractionCandidate {
	set := closureOf(r.units, m.site)

	symbols := make(map[string]bool)
	own := make(map[string]bool)
	needsExport := make(map[string]bool)
	lines := 0

	for site := range set {
		u := r.units[site]
		for n := range u.names {
			symbols[n] = true
			own[n] = own[n] || site == m.site
		}

		for _, d := range u.decls {
			lines += d.Lines
		}

		for n := range u.testRefs {
			needsExport[n] = true
		}
	}

	usedBy := 0

	var outside []string

	for site, u := range r.units {
		if set[site] {
			continue
		}

		used := false

		for ref := range u.refs {
			if !set[ref.To] {
				continue
			}

			used = true

			if !ref.Exported {
				needsExport[ref.Name] = true
			}

			if multiConfig && !r.primaryRefs[primaryRef{site, ref}] {
				outside = append(outside, fmt.Sprintf("%s is referenced from %s, outside the default build configuration; the move would not rewrite it", ref.Name, u.site.File))
			}
		}

		if used {
			usedBy++
		}
	}

	blockers := r.blockers[m.site]
	if blockers == nil {
		blockers = make(map[string]bool)
	}

	for _, b := range outside {
		blockers[b] = true
	}

	if multiConfig {
		for site := range set {
			for _, d := range r.units[site].decls {
				if !r.primaryDecls[d.Site] {
					blockers[fmt.Sprintf("%s is declared only outside the default build configuration, in %s", d.Name, d.Site.File)] = true
				}
			}
		}
	}

	c := ExtractionCandidate{
		Symbols:     slices.Sorted(maps.Keys(symbols)),
		Lines:       lines,
		UsedBy:      usedBy,
		NeedsExport: slices.AppendSeq([]string{}, maps.Keys(needsExport)),
		Line:        m.line,
		Configs:     m.configs,
		Blockers:    slices.AppendSeq([]string{}, maps.Keys(blockers)),
	}
	c.DependsOn = slices.DeleteFunc(slices.Clone(c.Symbols), func(n string) bool { return own[n] })
	slices.Sort(c.NeedsExport)
	slices.Sort(c.Blockers)
	c.Tier = len(c.DependsOn)
	c.File = m.site.File

	return c
}

// blockedRank sorts a candidate move_symbol would refuse after one it would not.
func blockedRank(c ExtractionCandidate) int {
	if len(c.Blockers) > 0 {
		return 1
	}

	return 0
}
