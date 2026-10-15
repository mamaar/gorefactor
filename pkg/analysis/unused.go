package analysis

import (
	"cmp"
	"fmt"
	"go/ast"
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
	// UnusedCallers are all the non-test declarations that refer to the symbol,
	// or to a method of it from outside the type. All of them are unused too.
	UnusedCallers []*types.Symbol
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
	g, idx, m, err := ua.prepare()
	if err != nil {
		return nil, err
	}
	m.markFrom(g, m.isRoot)

	r := &unusedReport{ua: ua, idx: idx, live: m.live, outside: outsideCallers(g, m.live), callers: deadCallers(g, m.live)}
	var unused []*UnusedSymbol
	for _, n := range g.nodes {
		if u := r.symbolOf(n); u != nil {
			unused = append(unused, u)
		}
	}
	slices.SortFunc(unused, compareUnused)

	return unused, nil
}

// prepare builds the symbol tables, the reference index (unless one was set),
// the usage graph and a marker for it.
func (ua *UnusedAnalyzer) prepare() (*usageGraph, *ReferenceIndex, *marker, error) {
	for _, pkg := range ua.workspace.Packages {
		if pkg.Symbols != nil {
			continue
		}
		if _, err := ua.resolver.BuildSymbolTable(pkg); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to build symbol table for package %s: %v", pkg.Path, err)
		}
	}

	idx := ua.index
	if idx == nil {
		idx = ua.resolver.BuildReferenceIndex()
	}

	g := buildUsageGraph(ua.workspace, ua.resolver, idx)
	m := &marker{
		ua:               ua,
		live:             make(map[*usageNode]bool, len(g.nodes)),
		from:             make(map[*usageNode]*usageNode),
		includeExported:  ua.includeExported,
		ifaceMethodNames: ua.collectInterfaceMethodNames(),
	}
	return g, idx, m, nil
}

func compareUnused(a, b *UnusedSymbol) int { return compareSymbols(a.Symbol, b.Symbol) }

// collectInterfaceMethodNames returns the names of the methods declared by any
// interface type in the workspace, named or not, plus well-known standard
// library ones. A method with such a name may be called through an interface.
func (ua *UnusedAnalyzer) collectInterfaceMethodNames() map[string]bool {
	names := make(map[string]bool)

	for _, common := range []string{
		"String", "Error", "Read", "Write", "Close", "Len", "Less", "Swap",
		"MarshalJSON", "UnmarshalJSON", "MarshalBinary", "UnmarshalBinary",
		"ServeHTTP", "RoundTrip",
	} {
		names[common] = true
	}

	c := &interfaceMethodCollector{names: names}
	for _, pkg := range ua.workspace.Packages {
		for _, file := range indexedFiles(pkg) {
			ast.Walk(c, file.AST)
		}
	}
	return names
}

// interfaceMethodCollector adds the method names of every interface type it
// visits to names.
type interfaceMethodCollector struct{ names map[string]bool }

func (c *interfaceMethodCollector) Visit(n ast.Node) ast.Visitor {
	iface, ok := n.(*ast.InterfaceType)
	if !ok || iface.Methods == nil {
		return c
	}
	for _, field := range iface.Methods.List {
		for _, name := range field.Names {
			c.names[name.Name] = true
		}
	}
	return c
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
	from             map[*usageNode]*usageNode // first node found to keep a node live; none for roots
	work             []*usageNode              // FIFO, so from gives shortest paths
	includeExported  bool
	followTests      bool // also follow test-to-production edges
	ifaceMethodNames map[string]bool
}

// markFrom marks every declaration reachable from the nodes of g that isRoot accepts.
func (m *marker) markFrom(g *usageGraph, isRoot func(*usageNode) bool) {
	for _, n := range g.nodes {
		if isRoot(n) {
			m.mark(n, nil)
		}
	}

	for len(m.work) > 0 {
		n := m.work[0]
		m.work = m.work[1:]
		m.visit(n)
	}
}

func (m *marker) mark(n, parent *usageNode) {
	if n == nil || m.live[n] {
		return
	}
	m.live[n] = true
	if parent != nil {
		m.from[n] = parent
	}
	m.work = append(m.work, n)
}

// visit marks what the live node n keeps alive: what it references, a
// method's receiver type, and the kept methods of a type.
func (m *marker) visit(n *usageNode) {
	for _, target := range n.out {
		m.mark(target, n)
	}
	if m.followTests {
		for _, target := range n.testOut {
			m.mark(target, n)
		}
	}
	m.mark(n.recv, n)
	for _, method := range n.methods {
		if m.isKeptMethod(method) {
			m.mark(method, n)
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
	return s.Exported && !m.includeExported
}

// isKeptMethod reports whether the method n is live whenever its receiver type
// is: it is exported and exported symbols are not reported, or it may
// implement an interface.
func (m *marker) isKeptMethod(n *usageNode) bool {
	s := n.symbol
	if s == nil || s.Kind != types.MethodSymbol {
		return false
	}
	return (s.Exported && !m.includeExported) || m.ifaceMethodNames[s.Name]
}

// unusedReport turns the dead nodes of a graph into findings.
type unusedReport struct {
	ua   *UnusedAnalyzer
	idx  *ReferenceIndex
	live map[*usageNode]bool
	// outside holds, for a type, the strongest cause among references to its
	// methods from outside the type.
	outside map[*usageNode]UnusedCause
	// callers holds, per dead node, the non-test nodes that refer to it.
	callers map[*usageNode][]*usageNode
}

// deadCallers inverts the reference edges that lead to dead nodes.
func deadCallers(g *usageGraph, live map[*usageNode]bool) map[*usageNode][]*usageNode {
	callers := make(map[*usageNode][]*usageNode)
	for _, n := range g.nodes {
		if n.test {
			continue
		}
		for _, target := range n.out {
			if !live[target] && !slices.Contains(callers[target], n) {
				callers[target] = append(callers[target], n)
			}
		}
	}
	return callers
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
		Symbol:        s,
		SafeToDelete:  !s.Exported && cause <= CauseOnlyDeclarations,
		Cause:         cause,
		Reason:        cause.String(),
		Referrers:     r.referrersOf(n),
		UnusedCallers: r.unusedCallersOf(n),
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

// referrersOf returns up to MaxReferrers declarations that refer to n, or to a
// method of n when they are outside the type.
func (r *unusedReport) referrersOf(n *usageNode) []*types.Symbol {
	var out []*types.Symbol
	for _, src := range append([]*usageNode{n}, n.methods...) {
		for _, ref := range src.referrers {
			if len(out) == MaxReferrers {
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

// unusedCallersOf returns the non-test declarations that refer to n, or to a
// method of n when they are outside the type, ordered by position.
func (r *unusedReport) unusedCallersOf(n *usageNode) []*types.Symbol {
	var out []*types.Symbol
	for _, src := range append([]*usageNode{n}, n.methods...) {
		for _, ref := range r.callers[src] {
			if ref.symbol == nil || ref == n || ref.recv == n || slices.Contains(out, ref.symbol) {
				continue
			}
			out = append(out, ref.symbol)
		}
	}
	slices.SortFunc(out, compareSymbols)
	return out
}

func compareSymbols(a, b *types.Symbol) int {
	return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Position, b.Position))
}
