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
	run := &extractionRun{detectRun: newDetectRun(req.Packages), units: make(map[analysis.DeclSite]*mergedUnit)}

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
	site      analysis.DeclSite
	line      int
	candidate bool
	names     map[string]bool
	decls     map[analysis.DeclSite]int
	refs      map[analysis.ExtractionRef]bool
	testRefs  map[string]bool
	configs   []string
}

type extractionRun struct {
	detectRun
	units map[analysis.DeclSite]*mergedUnit
}

func (r *extractionRun) visit(v *analysis.View) error {
	idx := r.indexOf(v)
	if !idx.HasTypeInfo() {
		return fmt.Errorf("no package has type information")
	}

	for _, pkg := range v.Workspace.Packages {
		if !r.wanted(pkg) {
			continue
		}

		for _, u := range analysis.ExtractionUnits(v.Workspace, v.Resolver, idx, pkg) {
			m := r.units[u.Site]
			if m == nil {
				m = &mergedUnit{
					site: u.Site, line: u.Line,
					names: map[string]bool{}, decls: map[analysis.DeclSite]int{},
					refs: map[analysis.ExtractionRef]bool{}, testRefs: map[string]bool{},
				}
				r.units[u.Site] = m
			}

			m.merge(u, v.Config.String())
		}
	}

	r.collectTypeErrors(v)
	r.planned(v, nil)

	return nil
}

func (m *mergedUnit) merge(u analysis.ExtractionUnit, config string) {
	m.candidate = m.candidate || u.Candidate
	m.configs = append(m.configs, config)

	for _, n := range u.Names {
		m.names[n] = true
	}

	for _, d := range u.Decls {
		m.decls[d.Site] = d.Lines
	}

	for _, ref := range u.Refs {
		m.refs[ref] = true
	}

	for _, n := range u.TestRefs {
		m.testRefs[n] = true
	}
}

func (r *extractionRun) report(req ExtractionCandidatesRequest, views []*analysis.View) *ExtractionCandidatesReport {
	out := &ExtractionCandidatesReport{DetectHeader: r.header(views)}

	for _, m := range r.units {
		if !m.candidate {
			continue
		}

		c := r.candidate(m)
		if len(c.Configs) == len(views) {
			c.Configs = nil
		}

		if req.MaxDependencies == nil || c.Tier <= *req.MaxDependencies {
			out.Candidates = append(out.Candidates, c)
		}
	}

	slices.SortFunc(out.Candidates, func(a, b ExtractionCandidate) int {
		return cmp.Or(cmp.Compare(a.Tier, b.Tier), cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})

	return out
}

// candidate computes what moving m drags along: the units it references,
// transitively.
func (r *extractionRun) candidate(m *mergedUnit) ExtractionCandidate {
	set := map[analysis.DeclSite]bool{m.site: true}

	for todo := []*mergedUnit{m}; len(todo) > 0; {
		cur := todo[0]
		todo = todo[1:]

		for ref := range cur.refs {
			next := r.units[ref.To]
			if next != nil && !set[ref.To] {
				set[ref.To] = true

				todo = append(todo, next)
			}
		}
	}

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

		for _, l := range u.decls {
			lines += l
		}

		for n := range u.testRefs {
			needsExport[n] = true
		}
	}

	usedBy := 0

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
		}

		if used {
			usedBy++
		}
	}

	c := ExtractionCandidate{
		Symbols:     slices.Sorted(maps.Keys(symbols)),
		Lines:       lines,
		UsedBy:      usedBy,
		NeedsExport: slices.AppendSeq([]string{}, maps.Keys(needsExport)),
		Line:        m.line,
		Configs:     m.configs,
	}
	c.DependsOn = slices.DeleteFunc(slices.Clone(c.Symbols), func(n string) bool { return own[n] })
	slices.Sort(c.NeedsExport)
	c.Tier = len(c.DependsOn)
	c.File = m.site.File

	return c
}
