package analysis

import (
	"go/build/constraint"
	"maps"
	"slices"
	"strings"
)

// maxCustomTags bounds the tag subsets tried for one file; wider constraints
// are reported as uncovered.
const maxCustomTags = 6

// UncoveredFile is a file that no build configuration can include.
type UncoveredFile struct {
	Path       string
	Constraint string
	Reason     string
}

// Covering is a small set of build configurations that together include every
// file in a source tree that any configuration can include.
type Covering struct {
	Configs   []BuildConfig // Configs[0] is the primary
	Files     []int         // Files[i]: files only Configs[i] matches (the primary: all it matches)
	Uncovered []UncoveredFile
}

// candidatePorts are tried after the primary's own GOOS/GOARCH.
var candidatePorts = [][2]string{
	{"linux", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}, {"ios", "arm64"},
	{"android", "arm64"}, {"freebsd", "amd64"}, {"js", "wasm"}, {"wasip1", "wasm"}, {"plan9", "amd64"},
}

var knownTags = func() map[string]bool {
	m := setOf("unix", "cgo", "gc", "gccgo")
	maps.Copy(m, knownOS)
	maps.Copy(m, knownArch)

	return m
}()

func isKnownTag(tag string) bool {
	return knownTags[tag] || strings.HasPrefix(tag, "go1.")
}

// ComputeCovering returns the primary plus the fewest extra configurations
// found that include every file the tree holds, and the files none can.
func ComputeCovering(tree *SourceTree, primary BuildConfig) Covering {
	var files []*SourceFile
	for _, dir := range tree.Dirs() {
		files = append(files, tree.Dir(dir)...)
	}

	slices.SortFunc(files, compareSourcePath)

	var uncovered []UncoveredFile
	var universe, pending []*SourceFile

	for _, f := range files {
		switch {
		case f.Err != nil:
			uncovered = append(uncovered, UncoveredFile{Path: f.Path, Reason: "parse error: " + f.Err.Error()})
		case primary.MatchesFile(f):
			universe = append(universe, f)
		default:
			pending = append(pending, f)
		}
	}

	var reqs []requirement

	for _, f := range pending {
		cfg, u := requirementFor(f, primary)
		if u != nil {
			uncovered = append(uncovered, *u)
			continue
		}

		reqs = append(reqs, requirement{cfg: cfg, file: f})
		universe = append(universe, f)
	}

	slices.SortFunc(uncovered, compareUncovered)

	configs := selectConfigs(mergeRequirements(reqs), universe, primary)

	return Covering{Configs: configs, Files: exclusiveCounts(configs, universe), Uncovered: uncovered}
}

func compareSourcePath(a, b *SourceFile) int { return strings.Compare(a.Path, b.Path) }

func compareUncovered(a, b UncoveredFile) int { return strings.Compare(a.Path, b.Path) }

// requirement is the cheapest configuration found that includes file.
type requirement struct {
	cfg  BuildConfig
	file *SourceFile
}

// requirementFor searches ports, cgo and custom-tag subsets, in that order, for
// the first configuration that includes f. A nil config comes with the reason.
func requirementFor(f *SourceFile, primary BuildConfig) (BuildConfig, *UncoveredFile) {
	expr := fileConstraint(f)

	u := &UncoveredFile{Path: f.Path}
	if expr != nil {
		u.Constraint = expr.String()
	}

	atoms := positiveCustomTags(expr)
	needsIgnore := slices.Contains(atoms, "ignore")

	atoms = slices.DeleteFunc(atoms, isIgnoreTag)
	if len(atoms) > maxCustomTags {
		u.Reason = "constraint too complex"
		return BuildConfig{}, u
	}

	subsets := subsetsBySize(atoms)

	for _, port := range portsFor(primary) {
		for _, cgo := range []bool{primary.Cgo, !primary.Cgo} {
			for _, subset := range subsets {
				cfg := NewBuildConfig(port[0], port[1], cgo, slices.Concat(primary.Tags, subset)...)
				if cfg.MatchesFile(f) {
					return cfg, nil
				}
			}
		}
	}

	u.Reason = "unsatisfiable build constraint"
	if needsIgnore {
		u.Reason = ignoreReason
	}

	return BuildConfig{}, u
}

const ignoreReason = "//go:build ignore"

func isIgnoreTag(tag string) bool { return tag == "ignore" }

func portsFor(primary BuildConfig) [][2]string {
	ports := [][2]string{{primary.GOOS, primary.GOARCH}}
	for _, p := range candidatePorts {
		if p != ports[0] {
			ports = append(ports, p)
		}
	}

	return ports
}

// fileConstraint returns the //go:build expression of f's header, else the
// conjunction of its // +build lines, else nil.
func fileConstraint(f *SourceFile) constraint.Expr {
	if f.AST == nil {
		return nil
	}
	var plus constraint.Expr

	for _, group := range f.AST.Comments {
		if group.End() >= f.AST.Package {
			break
		}

		for _, c := range group.List {
			switch {
			case constraint.IsGoBuild(c.Text):
				expr, err := constraint.Parse(c.Text)
				if err == nil {
					return expr
				}
			case constraint.IsPlusBuild(c.Text):
				expr, err := constraint.Parse(c.Text)
				if err == nil {
					plus = andExpr(plus, expr)
				}
			}
		}
	}

	return plus
}

func andExpr(a, b constraint.Expr) constraint.Expr {
	if a == nil {
		return b
	}

	return &constraint.AndExpr{X: a, Y: b}
}

// positiveCustomTags returns the sorted non-platform tags that appear
// un-negated in expr, the only ones worth setting to satisfy it.
func positiveCustomTags(expr constraint.Expr) []string {
	set := map[string]bool{}
	collectPositiveTags(expr, true, set)

	tags := make([]string, 0, len(set))
	for tag := range set {
		tags = append(tags, tag)
	}

	slices.Sort(tags)

	return tags
}

func collectPositiveTags(expr constraint.Expr, positive bool, set map[string]bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		if positive && !isKnownTag(e.Tag) {
			set[e.Tag] = true
		}
	case *constraint.NotExpr:
		collectPositiveTags(e.X, !positive, set)
	case *constraint.AndExpr:
		collectPositiveTags(e.X, positive, set)
		collectPositiveTags(e.Y, positive, set)
	case *constraint.OrExpr:
		collectPositiveTags(e.X, positive, set)
		collectPositiveTags(e.Y, positive, set)
	}
}

// subsetsBySize returns every subset of atoms, the empty one first, smaller
// subsets before larger ones.
func subsetsBySize(atoms []string) [][]string {
	var out [][]string
	for size := 0; size <= len(atoms); size++ {
		out = appendCombinations(out, atoms, size, nil)
	}

	return out
}

func appendCombinations(out [][]string, atoms []string, size int, prefix []string) [][]string {
	if size == 0 {
		return append(out, slices.Clone(prefix))
	}

	for i := 0; i+size <= len(atoms); i++ {
		out = appendCombinations(out, atoms[i+1:], size-1, append(prefix, atoms[i]))
	}

	return out
}

// mergedConfig is a candidate configuration with the files it was built for.
type mergedConfig struct {
	cfg   BuildConfig
	files []*SourceFile
}

// mergeRequirements first-fits each requirement into a config on the same
// GOOS/GOARCH/Cgo, unioning tags when every file already assigned still matches.
func mergeRequirements(reqs []requirement) []*mergedConfig {
	var merged []*mergedConfig
	for _, r := range reqs {
		m := firstFit(merged, r)
		if m != nil {
			m.cfg = unionTags(m.cfg, r.cfg)
			m.files = append(m.files, r.file)

			continue
		}

		merged = append(merged, &mergedConfig{cfg: r.cfg, files: []*SourceFile{r.file}})
	}

	return merged
}

func firstFit(merged []*mergedConfig, r requirement) *mergedConfig {
	for _, m := range merged {
		if m.cfg.GOOS != r.cfg.GOOS || m.cfg.GOARCH != r.cfg.GOARCH || m.cfg.Cgo != r.cfg.Cgo {
			continue
		}

		union := unionTags(m.cfg, r.cfg)
		if union.MatchesFile(r.file) && matchesAll(union, m.files) {
			return m
		}
	}

	return nil
}

func unionTags(a, b BuildConfig) BuildConfig {
	return NewBuildConfig(a.GOOS, a.GOARCH, a.Cgo, slices.Concat(a.Tags, b.Tags)...)
}

func matchesAll(cfg BuildConfig, files []*SourceFile) bool {
	return !slices.ContainsFunc(files, cfg.notMatching)
}

func (c BuildConfig) notMatching(f *SourceFile) bool { return !c.MatchesFile(f) }

// selectConfigs returns the primary followed by a greedy cover of the
// universe files the primary does not match, ordered by Key.
func selectConfigs(candidates []*mergedConfig, universe []*SourceFile, primary BuildConfig) []BuildConfig {
	remaining := slices.DeleteFunc(slices.Clone(universe), primary.MatchesFile)
	var chosen []BuildConfig

	for len(remaining) > 0 {
		best, bestCount := BuildConfig{}, 0

		for _, m := range candidates {
			count := countMatches(m.cfg, remaining)
			if count > bestCount || (count == bestCount && count > 0 && lessConfig(m.cfg, best, primary)) {
				best, bestCount = m.cfg, count
			}
		}

		if bestCount == 0 {
			break
		}

		chosen = append(chosen, best)
		remaining = slices.DeleteFunc(remaining, best.MatchesFile)
	}

	slices.SortFunc(chosen, compareConfigKey)

	return append([]BuildConfig{primary}, chosen...)
}

func countMatches(cfg BuildConfig, files []*SourceFile) int {
	n := 0

	for _, f := range files {
		if cfg.MatchesFile(f) {
			n++
		}
	}

	return n
}

// lessConfig orders by fewer deltas from primary, then Key.
func lessConfig(a, b, primary BuildConfig) bool {
	da, db := deltas(a, primary), deltas(b, primary)
	if da != db {
		return da < db
	}

	return a.Key() < b.Key()
}

func deltas(c, primary BuildConfig) int {
	n := 0
	if c.GOOS != primary.GOOS {
		n++
	}

	if c.GOARCH != primary.GOARCH {
		n++
	}

	if c.Cgo != primary.Cgo {
		n++
	}

	for _, tag := range c.Tags {
		if !slices.Contains(primary.Tags, tag) {
			n++
		}
	}

	return n
}

func compareConfigKey(a, b BuildConfig) int { return strings.Compare(a.Key(), b.Key()) }

// exclusiveCounts returns, per config, the universe files only it matches; the
// primary (index 0) counts every file it matches.
func exclusiveCounts(configs []BuildConfig, universe []*SourceFile) []int {
	counts := make([]int, len(configs))
	for _, f := range universe {
		matching := matchingIndexes(configs, f)
		switch {
		case len(matching) == 0:
		case matching[0] == 0:
			counts[0]++
		case len(matching) == 1:
			counts[matching[0]]++
		}
	}

	return counts
}

func matchingIndexes(configs []BuildConfig, f *SourceFile) []int {
	var out []int

	for i, c := range configs {
		if c.MatchesFile(f) {
			out = append(out, i)
		}
	}

	return out
}
