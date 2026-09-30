package refactor

import (
	"fmt"
	"sync"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// maxConfigRounds bounds the fixpoint over build configurations. Every round
// but the last adds a key, so reaching the bound means a planner misbehaves.
const maxConfigRounds = 1024

// configResult is one view's outcome of a configPlanner.
type configResult struct {
	plan *types.RefactoringPlan
	// changed holds the keys of the symbols the plan alters in a way their
	// counterparts in other views must follow.
	changed types.SymbolKeySet
	// absent is set instead of plan when the operation's target does not exist
	// in the view and no linked key resolves there.
	absent error
	reach  reach
	// declared holds the keys of the functions the view declares. Linking any
	// other key cannot change the view's plan; nil when unknown.
	declared types.SymbolKeySet
}

// declaredFuncKeys returns the keys of the functions and interface methods ws
// declares, which are the declarations a linked key can name.
func declaredFuncKeys(ws *types.Workspace) types.SymbolKeySet {
	keys := make(types.SymbolKeySet)
	for fn := range buildFuncDeclIndex(ws) {
		if key, ok := types.SymbolKeyOf(fn); ok {
			keys[key] = struct{}{}
		}
	}
	return keys
}

// configPlanner plans one operation in one view, given the keys linked from
// other views. Linking a key the view already reported must not change its plan.
type configPlanner interface {
	planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error)
}

// maxPreparedSecondaries bounds how many views besides the primary hold type
// information at once during a run; each holds roughly as much as the primary.
const maxPreparedSecondaries = 2

// viewIndexes holds the reference indexes of the views one run plans in.
// Within planAcrossConfigs it prepares views ahead of their plan and drops
// each secondary view once planned; used alone it builds a view's index on
// request and keeps it. Only the planning goroutine calls it.
type viewIndexes struct {
	indexes map[*analysis.View]*analysis.ReferenceIndex

	views   []*analysis.View // of the run, primary first; nil outside one
	preps   map[*analysis.View]*viewPrep
	dropped map[*analysis.View]bool
	warming sync.WaitGroup
}

// viewPrep is one view being type-checked and indexed.
type viewPrep struct {
	done  chan struct{}
	index *analysis.ReferenceIndex
}

func (p *viewPrep) run(v *analysis.View) {
	defer close(p.done)
	p.index = checkedIndex(v)
}

// checkedIndex type-checks v and builds its reference index. The view's own
// cached index is not used: it may predate type-checking.
func checkedIndex(v *analysis.View) *analysis.ReferenceIndex {
	ensureTypeChecked(v.Parser, v.Workspace)
	return v.Resolver.BuildLazyReferenceIndex()
}

// prepare starts type-checking the primary and the first secondary views, and
// loads the third-party packages of the others meanwhile.
func (c *viewIndexes) prepare(views []*analysis.View) {
	c.views = views
	c.preps = make(map[*analysis.View]*viewPrep)
	c.dropped = make(map[*analysis.View]bool)
	c.start(views[0])
	c.fill()
	for _, v := range views[1:] {
		if c.preps[v] == nil {
			c.warming.Add(1)
			go c.warm(v)
		}
	}
}

func (c *viewIndexes) warm(v *analysis.View) {
	defer c.warming.Done()
	v.WarmExternals()
}

func (c *viewIndexes) start(v *analysis.View) *viewPrep {
	p := &viewPrep{done: make(chan struct{})}
	c.preps[v] = p
	go p.run(v)
	return p
}

// fill starts preparing secondary views not planned yet, in order, up to the bound.
func (c *viewIndexes) fill() {
	for _, v := range c.views[1:] {
		if c.heldSecondaries() >= maxPreparedSecondaries {
			return
		}
		if c.preps[v] == nil && c.indexes[v] == nil && !c.dropped[v] {
			c.start(v)
		}
	}
}

// heldSecondaries counts the secondary views that hold type information or
// are being type-checked.
func (c *viewIndexes) heldSecondaries() int {
	n := 0
	for _, v := range c.views[1:] {
		if c.preps[v] != nil || c.indexes[v] != nil {
			n++
		}
	}
	return n
}

// indexOf returns v's index, waiting for or building it as needed.
func (c *viewIndexes) indexOf(v *analysis.View) *analysis.ReferenceIndex {
	if idx := c.indexes[v]; idx != nil {
		return idx
	}
	if c.indexes == nil {
		c.indexes = make(map[*analysis.View]*analysis.ReferenceIndex)
	}
	if p := c.preps[v]; p != nil {
		<-p.done
		delete(c.preps, v)
		c.indexes[v] = p.index
		return p.index
	}
	if c.dropped[v] {
		// A dropped view still counts as checked.
		v.ReleaseTypeInfo()
		delete(c.dropped, v)
	}
	c.indexes[v] = checkedIndex(v)
	return c.indexes[v]
}

// planned drops the type information of a secondary view of the run once
// plan, its plan, is made, and starts preparing the next one. The view keeps
// what coverage reporting reads, and is type-checked again if it is replanned.
func (c *viewIndexes) planned(v *analysis.View, plan *types.RefactoringPlan) {
	if len(c.views) == 0 || v == c.views[0] {
		return
	}
	if plan != nil {
		for _, op := range plan.Operations {
			if h, ok := op.(analysisHolder); ok {
				h.dropAnalysis()
			}
		}
	}
	delete(c.indexes, v)
	v.DropTypeInfo()
	c.dropped[v] = true
	c.fill()
}

// analysisHolder is an operation that keeps the type information it planned
// from reachable.
type analysisHolder interface {
	dropAnalysis()
}

// finish waits for the views still being prepared and releases the type
// information of every secondary view.
func (c *viewIndexes) finish() {
	c.warming.Wait()
	for _, p := range c.preps {
		<-p.done
	}
	for _, v := range c.views[1:] {
		v.ReleaseTypeInfo()
	}
	c.views, c.preps, c.dropped = nil, nil, nil
}

// indexedPlanner is a configPlanner that plans from per-view reference indexes.
type indexedPlanner interface {
	configPlanner
	prepare(views []*analysis.View)
	finish()
}

// planAcrossConfigs runs p under every build configuration of the covering set
// and merges the per-view plans into one.
func (e *DefaultEngine) planAcrossConfigs(p indexedPlanner) (*types.RefactoringPlan, error) {
	if e.set == nil {
		return nil, fmt.Errorf("no workspace loaded")
	}
	// One run at a time: a run releases the secondary views it planned in.
	e.configRuns.Lock()
	defer e.configRuns.Unlock()

	views, err := e.set.Views()
	if err != nil {
		return nil, err
	}
	p.prepare(views)
	defer p.finish()
	plan, rounds, err := planAcrossViews(views, p, maxConfigRounds)
	if err != nil {
		return nil, err
	}
	e.logger.Info("planned across build configurations",
		"configs", len(views), "rounds", rounds, "changes", len(plan.Changes))
	return plan, nil
}

// planAcrossViews replans the views until no view has linked keys it has not
// planned with, then merges each view's latest plan. It also returns the number
// of rounds in which some view was planned.
func planAcrossViews(views []*analysis.View, p configPlanner, maxRounds int) (*types.RefactoringPlan, int, error) {
	run := &configRun{
		views:   views,
		planner: p,
		linked:  make(types.SymbolKeySet),
		results: make([]configResult, len(views)),
		seen:    make([]types.SymbolKeySet, len(views)),
	}
	rounds := 0
	for run.anyStale() {
		if rounds == maxRounds {
			return nil, rounds, &types.RefactorError{
				Type: types.InvalidOperation,
				Message: fmt.Sprintf(
					"internal error: build configurations did not settle after %d rounds (%d linked symbols)",
					maxRounds, len(run.linked)),
			}
		}
		rounds++
		if err := run.round(); err != nil {
			return nil, rounds, err
		}
	}
	plan, err := run.merge()
	return plan, rounds, err
}

// configRun is the state of one fixpoint over a set of views.
type configRun struct {
	views   []*analysis.View
	planner configPlanner
	linked  types.SymbolKeySet
	results []configResult
	// seen[i] holds the keys view i has accounted for: those it was last planned
	// with and those it reported itself. nil before its first plan.
	seen []types.SymbolKeySet
}

// stale reports whether view i has not been planned, or a key was linked since
// that it may declare.
func (r *configRun) stale(i int) bool {
	if r.seen[i] == nil {
		return true
	}
	declared := r.results[i].declared
	for key := range r.linked {
		if !r.seen[i].Has(key) && (declared == nil || declared.Has(key)) {
			return true
		}
	}
	return false
}

func (r *configRun) anyStale() bool {
	for i := range r.views {
		if r.stale(i) {
			return true
		}
	}
	return false
}

// round plans every stale view with the keys linked so far.
func (r *configRun) round() error {
	for i, v := range r.views {
		if !r.stale(i) {
			continue
		}
		res, err := r.planner.planIn(v, r.linked)
		if err != nil {
			return r.configError(v, err)
		}
		r.results[i] = res
		r.seen[i] = r.linked.Clone()
		r.seen[i].AddAll(res.changed)
		r.linked.AddAll(res.changed)
	}
	return nil
}

// configError names the build configuration err came from, unless there is
// only one.
func (r *configRun) configError(v *analysis.View, err error) error {
	if len(r.views) == 1 {
		return err
	}
	return fmt.Errorf("build configuration %s: %w", v.Config, err)
}

// merge combines the views' plans. When the target is absent from every view
// it returns the first view's error, which is the primary's.
func (r *configRun) merge() (*types.RefactoringPlan, error) {
	var plans []ConfigPlan
	var absent error
	configs := make([]string, 0, len(r.views))
	for i, v := range r.views {
		name := v.Config.String()
		configs = append(configs, name)
		res := r.results[i]
		if res.plan != nil {
			plans = append(plans, ConfigPlan{Config: name, Plan: res.plan})
		} else if absent == nil {
			absent = res.absent
		}
	}
	if len(plans) == 0 && absent != nil {
		return nil, absent
	}
	merged, err := MergeConfigPlans(plans)
	if err != nil {
		return nil, err
	}
	merged.Coverage = types.Coverage{Configs: configs}
	if err := r.reportUncovered(merged); err != nil {
		return nil, err
	}
	return merged, nil
}
