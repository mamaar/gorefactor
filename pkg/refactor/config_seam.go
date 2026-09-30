package refactor

import (
	"fmt"

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
}

// configPlanner plans one operation in one view, given the keys linked from
// other views. Linking a key the view already reported must not change its plan.
type configPlanner interface {
	planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error)
}

// planAcrossConfigs runs p under every build configuration of the covering set
// and merges the per-view plans into one.
func (e *DefaultEngine) planAcrossConfigs(p configPlanner) (*types.RefactoringPlan, error) {
	if e.set == nil {
		return nil, fmt.Errorf("no workspace loaded")
	}
	views, err := e.set.Views()
	if err != nil {
		return nil, err
	}
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

func (r *configRun) stale(i int) bool {
	return r.seen[i] == nil || !r.seen[i].ContainsAll(r.linked)
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
