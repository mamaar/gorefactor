package analysis

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"

	"github.com/mamaar/gorefactor/pkg/types"
)

// UnusedCause says why a symbol is unused. Later constants are stronger
// evidence that something still refers to it.
type UnusedCause int

const (
	CauseNoReferences UnusedCause = iota
	CauseOnlyDeclarations
	CauseReceiverUnused
	CauseOnlyTests
	CauseOnlyUnusedCode
)

var causeReasons = [...]string{
	CauseNoReferences:     "No references found",
	CauseOnlyDeclarations: "Only referenced in declarations",
	CauseReceiverUnused:   "Receiver type is unused",
	CauseOnlyTests:        "Only referenced from tests",
	CauseOnlyUnusedCode:   "Only referenced from unused code",
}

func (c UnusedCause) String() string { return causeReasons[c] }

// UnusedSymbol represents an unused symbol in the codebase
type UnusedSymbol struct {
	Symbol       *types.Symbol
	SafeToDelete bool // True if unexported and safe to remove
	Cause        UnusedCause
	Reason       string // Cause as text
	// Referrers are up to five declarations that refer to the symbol without
	// keeping it alive: unused code and tests.
	Referrers []*types.Symbol
}

// UnusedAnalyzer finds unused symbols in the workspace
type UnusedAnalyzer struct {
	workspace       *types.Workspace
	resolver        *SymbolResolver
	includeExported bool
	index           *ReferenceIndex
}

// SetIndex makes the analyzer use idx instead of building a reference index.
func (ua *UnusedAnalyzer) SetIndex(idx *ReferenceIndex) {
	ua.index = idx
}

// SetIncludeExported controls whether exported symbols are included in the analysis
func (ua *UnusedAnalyzer) SetIncludeExported(include bool) {
	ua.includeExported = include
}

// NewUnusedAnalyzer creates a new unused symbol analyzer
func NewUnusedAnalyzer(ws *types.Workspace, logger *slog.Logger) *UnusedAnalyzer {
	return &UnusedAnalyzer{
		workspace: ws,
		resolver:  NewSymbolResolver(ws, logger),
	}
}

// FindUnusedSymbols finds all unused symbols in the workspace. A symbol is
// unused unless live code outside test files references it; code that only
// unused code or tests reference is unused too.
func (ua *UnusedAnalyzer) FindUnusedSymbols() ([]*UnusedSymbol, error) {
	for _, pkg := range ua.workspace.Packages {
		if pkg.Symbols != nil {
			continue
		}
		if _, err := ua.resolver.BuildSymbolTable(pkg); err != nil {
			return nil, fmt.Errorf("failed to build symbol table for package %s: %v", pkg.Path, err)
		}
	}

	idx := ua.index
	if idx == nil {
		idx = ua.resolver.BuildReferenceIndex()
	}

	g := buildUsageGraph(ua.workspace, ua.resolver, idx)
	m := &marker{ua: ua, live: make(map[*usageNode]bool, len(g.nodes)), ifaceMethodNames: ua.collectInterfaceMethodNames()}
	m.markFrom(g)

	r := &unusedReport{ua: ua, idx: idx, live: m.live, outside: outsideCallers(g, m.live)}
	var unused []*UnusedSymbol
	for _, n := range g.nodes {
		if u := r.symbolOf(n); u != nil {
			unused = append(unused, u)
		}
	}
	slices.SortFunc(unused, compareUnused)

	return unused, nil
}

func compareUnused(a, b *UnusedSymbol) int {
	return cmp.Or(
		cmp.Compare(a.Symbol.File, b.Symbol.File),
		cmp.Compare(a.Symbol.Position, b.Symbol.Position),
	)
}

// collectInterfaceMethodNames builds a set of all method names declared in interfaces
// across the workspace. This is computed once and used for O(1) lookups in
// mightImplementInterface instead of repeated AST walks.
func (ua *UnusedAnalyzer) collectInterfaceMethodNames() map[string]bool {
	names := make(map[string]bool)

	// Always include well-known interface methods
	for _, common := range []string{
		"String", "Error", "Read", "Write", "Close", "Len", "Less", "Swap",
		"MarshalJSON", "UnmarshalJSON", "MarshalBinary", "UnmarshalBinary",
		"ServeHTTP", "RoundTrip",
	} {
		names[common] = true
	}

	for _, pkg := range ua.workspace.Packages {
		if pkg.Symbols == nil {
			continue
		}
		for _, symbol := range pkg.Symbols.Types {
			if symbol.Kind != types.InterfaceSymbol {
				continue
			}
			ifaceMethods, err := ua.resolver.getInterfaceMethods(symbol)
			if err != nil {
				continue
			}
			for _, m := range ifaceMethods {
				names[m.Name] = true
			}
		}
	}

	return names
}

// isTestFunction checks if a function is a test function
func (ua *UnusedAnalyzer) isTestFunction(symbol *types.Symbol) bool {
	if symbol.Kind != types.FunctionSymbol {
		return false
	}

	// Check if it's in a test file
	if len(symbol.File) > 8 && symbol.File[len(symbol.File)-8:] == "_test.go" {
		return true
	}

	// Check common test function patterns
	testPrefixes := []string{"Test", "Benchmark", "Example", "Fuzz"}
	for _, prefix := range testPrefixes {
		if len(symbol.Name) >= len(prefix) && symbol.Name[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}

// GetUnusedUnexportedSymbols returns only unexported unused symbols that are safe to delete
func (ua *UnusedAnalyzer) GetUnusedUnexportedSymbols() ([]*UnusedSymbol, error) {
	allUnused, err := ua.FindUnusedSymbols()
	if err != nil {
		return nil, err
	}

	var safeToDelete []*UnusedSymbol
	for _, unused := range allUnused {
		if unused.SafeToDelete && !unused.Symbol.Exported {
			safeToDelete = append(safeToDelete, unused)
		}
	}

	return safeToDelete, nil
}

// FormatUnusedSymbol formats an unused symbol for display
func (ua *UnusedAnalyzer) FormatUnusedSymbol(unused *UnusedSymbol) string {
	symbol := unused.Symbol
	return fmt.Sprintf("%s %s (%s:%d:%d) - %s",
		symbol.Kind.String(),
		symbol.Name,
		symbol.File,
		symbol.Line,
		symbol.Column,
		unused.Reason)
}

// marker finds the live declarations of a graph.
type marker struct {
	ua               *UnusedAnalyzer
	live             map[*usageNode]bool
	work             []*usageNode
	ifaceMethodNames map[string]bool
}

// markFrom marks every declaration reachable from the roots of g.
func (m *marker) markFrom(g *usageGraph) {
	for _, n := range g.nodes {
		if m.isRoot(n) {
			m.mark(n)
		}
	}

	for len(m.work) > 0 {
		n := m.work[len(m.work)-1]
		m.work = m.work[:len(m.work)-1]
		m.visit(n)
	}
}

func (m *marker) mark(n *usageNode) {
	if n == nil || m.live[n] {
		return
	}
	m.live[n] = true
	m.work = append(m.work, n)
}

// visit marks what the live node n keeps alive: what it references, a
// method's receiver type, and the kept methods of a type.
func (m *marker) visit(n *usageNode) {
	for _, target := range n.out {
		m.mark(target)
	}
	m.mark(n.recv)
	for _, method := range n.methods {
		if m.isKeptMethod(method) {
			m.mark(method)
		}
	}
}

// isRoot reports whether n is live regardless of references.
func (m *marker) isRoot(n *usageNode) bool {
	switch n.name {
	case "main", "init", "_":
		return true
	}
	s := n.symbol
	if s == nil {
		return false
	}
	if m.ua.isTestFunction(s) {
		return true
	}
	if s.Kind == types.MethodSymbol {
		return n.recv == nil && m.isKeptMethod(n)
	}
	return s.Exported && !m.ua.includeExported
}

// isKeptMethod reports whether the method n is live whenever its receiver type
// is: it is exported and exported symbols are not reported, or it may
// implement an interface.
func (m *marker) isKeptMethod(n *usageNode) bool {
	s := n.symbol
	if s == nil || s.Kind != types.MethodSymbol {
		return false
	}
	return (s.Exported && !m.ua.includeExported) || m.ifaceMethodNames[s.Name]
}

// unusedReport turns the dead nodes of a graph into findings.
type unusedReport struct {
	ua   *UnusedAnalyzer
	idx  *ReferenceIndex
	live map[*usageNode]bool
	// outside holds, for a type, the strongest cause among references to its
	// methods from outside the type.
	outside map[*usageNode]UnusedCause
}

// outsideCallers finds, per dead receiver type, how its methods are referred
// to from outside the type's own methods.
func outsideCallers(g *usageGraph, live map[*usageNode]bool) map[*usageNode]UnusedCause {
	outside := make(map[*usageNode]UnusedCause)
	for _, n := range g.nodes {
		for _, target := range n.out {
			typ := target.recv
			if typ == nil || typ == n.recv || live[typ] {
				continue
			}
			outside[typ] = CauseOnlyUnusedCode
		}
		if n.recv != nil && !live[n.recv] && n.testRefs > 0 && outside[n.recv] < CauseOnlyTests {
			outside[n.recv] = CauseOnlyTests
		}
	}
	return outside
}

// symbolOf returns the finding for n, or nil when n is live or not reportable.
func (r *unusedReport) symbolOf(n *usageNode) *UnusedSymbol {
	s := n.symbol
	if s == nil || r.live[n] {
		return nil
	}
	if s.Exported && !r.ua.includeExported {
		return nil
	}

	cause := r.causeOf(n)
	return &UnusedSymbol{
		Symbol:       s,
		SafeToDelete: !s.Exported && cause <= CauseOnlyDeclarations,
		Cause:        cause,
		Reason:       cause.String(),
		Referrers:    r.referrersOf(n),
	}
}

func (r *unusedReport) causeOf(n *usageNode) UnusedCause {
	switch {
	case n.prodRefs > 0:
		return CauseOnlyUnusedCode
	case n.testRefs > 0:
		return CauseOnlyTests
	}

	cause := r.declarationCause(n)
	if n.symbol.Kind == types.MethodSymbol && n.recv != nil && !r.live[n.recv] {
		cause = CauseReceiverUnused
	}
	return max(cause, r.outside[n])
}

// declarationCause tells whether anything but the declaration itself mentions
// the symbol.
func (r *unusedReport) declarationCause(n *usageNode) UnusedCause {
	s := n.symbol
	if targetObj := r.ua.resolver.resolveTypesObject(s); targetObj != nil {
		if objEntries := r.idx.ObjectEntries(targetObj); len(objEntries) > 0 {
			for i := range objEntries {
				if objEntries[i].Pos != s.Position {
					return CauseOnlyDeclarations
				}
			}
			return CauseNoReferences
		}
	}

	entries, _ := r.idx.NameEntries(s.Name)
	for i := range entries {
		entry := &entries[i]
		if entry.Pos == s.Position {
			continue
		}
		if entry.IsSelector {
			if r.ua.resolver.importAliasRefersToPackage(entry.PkgAlias, entry.File, s.Package) {
				return CauseOnlyDeclarations
			}
		} else if r.ua.resolver.isSamePackage(entry.File.Package, s.Package) {
			return CauseOnlyDeclarations
		}
	}
	return CauseNoReferences
}

// referrersOf returns up to maxReferrers declarations that refer to n, or to a
// method of n when they are outside the type.
func (r *unusedReport) referrersOf(n *usageNode) []*types.Symbol {
	var out []*types.Symbol
	for _, src := range append([]*usageNode{n}, n.methods...) {
		for _, ref := range src.referrers {
			if len(out) == maxReferrers {
				return out
			}
			if ref.symbol == nil || ref.recv == n || slices.Contains(out, ref.symbol) {
				continue
			}
			out = append(out, ref.symbol)
		}
	}
	return out
}
