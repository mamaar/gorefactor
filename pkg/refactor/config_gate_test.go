package refactor

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// fakeGate is a fakePlanner that records the blockers it is asked about and
// refuses with err when there are any.
type fakeGate struct {
	fakePlanner
	err  error
	seen [][]types.Blocker
}

func (g *fakeGate) refuse(blockers []types.Blocker) error {
	g.seen = append(g.seen, blockers)
	if len(blockers) == 0 {
		return nil
	}
	return g.err
}

func blockerAt(file string, offset int) types.Blocker {
	return types.Blocker{File: file, Offset: offset}
}

func TestPlanAcrossViews_BlockerInAnyViewStopsTheMerge(t *testing.T) {
	primary, tagged := fakeViews()
	refused := errors.New("refused")
	gate := &fakeGate{err: refused}
	gate.plan = func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		res := configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a"))}
		if v == tagged {
			res.blockers = []types.Blocker{blockerAt("/ws/use_tagx.go", 7)}
		}
		return res, nil
	}

	plan, _, err := planAcrossViews([]*analysis.View{primary, tagged}, gate, 10)

	if err != refused || plan != nil {
		t.Fatalf("plan = %v, err = %v; want the gate's refusal and no plan", plan, err)
	}
	want := []types.Blocker{{File: "/ws/use_tagx.go", Offset: 7, Configs: []string{tagged.Config.String()}}}
	if len(gate.seen) != 1 || !reflect.DeepEqual(gate.seen[0], want) {
		t.Errorf("gate saw %+v, want %+v", gate.seen, want)
	}
	if len(gate.calls[primary]) != 1 || len(gate.calls[tagged]) != 1 {
		t.Errorf("views planned %d/%d times, want every view planned before the gate", len(gate.calls[primary]), len(gate.calls[tagged]))
	}
}

func TestPlanAcrossViews_NoBlockersMergesThePlans(t *testing.T) {
	primary, tagged := fakeViews()
	gate := &fakeGate{err: errors.New("refused")}
	gate.plan = func(*analysis.View, types.SymbolKeySet) (configResult, error) {
		return configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a"))}, nil
	}

	plan, _, err := planAcrossViews([]*analysis.View{primary, tagged}, gate, 10)

	if err != nil || len(plan.Changes) != 1 {
		t.Fatalf("plan = %+v, err = %v; want the merged plan", plan, err)
	}
	if len(gate.seen) != 1 || len(gate.seen[0]) != 0 {
		t.Errorf("gate saw %+v, want one call without blockers", gate.seen)
	}
}

func TestPlanAcrossViews_GateThatDoesNotRefuseKeepsThePlan(t *testing.T) {
	primary, tagged := fakeViews()
	gate := &fakeGate{}
	gate.plan = func(*analysis.View, types.SymbolKeySet) (configResult, error) {
		return configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a")), blockers: []types.Blocker{blockerAt("/ws/use.go", 3)}}, nil
	}

	plan, _, err := planAcrossViews([]*analysis.View{primary, tagged}, gate, 10)

	if err != nil || len(plan.Changes) != 1 {
		t.Fatalf("plan = %+v, err = %v; want the merged plan", plan, err)
	}
}

func TestBlockersAcrossViews_OneBlockerPerSiteWithoutConfigsWhenEveryViewSeesIt(t *testing.T) {
	primary, tagged := fakeViews()
	gate := &fakeGate{}
	gate.plan = func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		res := configResult{plan: planOf(), blockers: []types.Blocker{blockerAt("/ws/use.go", 3)}}
		if v == primary {
			res.blockers = append(res.blockers, blockerAt("/ws/use_default.go", 9))
		}
		return res, nil
	}

	got, err := blockersAcrossViews([]*analysis.View{primary, tagged}, gate, 10)

	if err != nil {
		t.Fatal(err)
	}
	want := []types.Blocker{
		blockerAt("/ws/use.go", 3),
		{File: "/ws/use_default.go", Offset: 9, Configs: []string{primary.Config.String()}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blockers = %+v, want %+v", got, want)
	}
	if len(gate.seen) != 0 {
		t.Errorf("gate was asked to refuse %d times, want none for a query", len(gate.seen))
	}
}

func TestBlockersAcrossViews_TargetAbsentEverywhereReturnsThePrimaryError(t *testing.T) {
	primary, tagged := fakeViews()
	primaryErr := errors.New("not in primary")
	gate := &fakeGate{}
	gate.plan = func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		if v == primary {
			return configResult{absent: primaryErr}, nil
		}
		return configResult{absent: errors.New("not in tagged")}, nil
	}

	_, err := blockersAcrossViews([]*analysis.View{primary, tagged}, gate, 10)
	if err != primaryErr {
		t.Errorf("err = %v, want the primary view's error unchanged", err)
	}
}

func TestBlockersAcrossViews_PlannerErrorNamesItsConfig(t *testing.T) {
	primary, tagged := fakeViews()
	gate := &fakeGate{}
	gate.plan = func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		if v == tagged {
			return configResult{}, errors.New("boom")
		}
		return configResult{plan: planOf()}, nil
	}

	_, err := blockersAcrossViews([]*analysis.View{primary, tagged}, gate, 10)

	want := "build configuration " + tagged.Config.String() + ": boom"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}
