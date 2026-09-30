package refactor

import (
	"fmt"

	"github.com/mamaar/gorefactor/pkg/types"
)

// ConfigPlan is one build configuration's plan, labelled for error messages.
type ConfigPlan struct {
	Config string // display name, e.g. "default", "tags=fts5", "GOOS=windows"
	Plan   *types.RefactoringPlan
}

// changeIdentity is what makes two changes the same edit. A whole-file
// deletion is identified by its file alone.
type changeIdentity struct {
	file    string
	start   int
	end     int
	newText string
	del     bool
}

func identityOf(c types.Change) changeIdentity {
	if c.Delete {
		return changeIdentity{file: c.File, del: true}
	}
	return changeIdentity{file: c.File, start: c.Start, end: c.End, newText: c.NewText}
}

// mergedChange is a kept change with the indexes of the plans that contain its identity.
type mergedChange struct {
	change types.Change
	id     changeIdentity
	owners []int
}

// MergeConfigPlans merges per-configuration plans into one. Each distinct
// change is kept as many times as the single configuration that emits it most
// often; changes that disagree across configurations are reported as an error.
func MergeConfigPlans(plans []ConfigPlan) (*types.RefactoringPlan, error) {
	var live []ConfigPlan
	for _, p := range plans {
		if p.Plan != nil {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "cannot merge build-configuration plans: no plans given",
		}
	}

	merged := mergeConfigChanges(live)
	if err := findConfigConflict(merged, live); err != nil {
		return nil, err
	}

	out := &types.RefactoringPlan{
		Operations: live[0].Plan.Operations,
		Reversible: true,
	}
	for _, m := range merged {
		out.Changes = append(out.Changes, m.change)
	}
	seenFiles := make(map[string]bool)
	seenSites := make(map[types.SkippedSite]bool)
	for _, p := range live {
		if out.Impact == nil {
			out.Impact = p.Plan.Impact
		}
		out.Reversible = out.Reversible && p.Plan.Reversible
		for _, f := range p.Plan.AffectedFiles {
			if !seenFiles[f] {
				seenFiles[f] = true
				out.AffectedFiles = append(out.AffectedFiles, f)
			}
		}
		for _, s := range p.Plan.SkippedSites {
			if !seenSites[s] {
				seenSites[s] = true
				out.SkippedSites = append(out.SkippedSites, s)
			}
		}
	}
	return out, nil
}

// mergeConfigChanges keeps each identity as often as its most frequent plan emits it,
// in first-seen order.
func mergeConfigChanges(plans []ConfigPlan) []*mergedChange {
	var kept []*mergedChange
	keptCount := make(map[changeIdentity]int)
	firstOfID := make(map[changeIdentity][]*mergedChange)
	for pi, p := range plans {
		local := make(map[changeIdentity]int)
		for _, c := range p.Plan.Changes {
			id := identityOf(c)
			local[id]++
			if local[id] > keptCount[id] {
				m := &mergedChange{change: c, id: id}
				kept = append(kept, m)
				keptCount[id]++
				firstOfID[id] = append(firstOfID[id], m)
			}
		}
		for id := range local {
			for _, m := range firstOfID[id] {
				if n := len(m.owners); n == 0 || m.owners[n-1] != pi {
					m.owners = append(m.owners, pi)
				}
			}
		}
	}
	return kept
}

// findConfigConflict returns an error for the first pair of kept changes that
// conflict and that no single configuration emits together.
func findConfigConflict(merged []*mergedChange, plans []ConfigPlan) error {
	serializer := NewSerializer()
	for i, a := range merged {
		for _, b := range merged[i+1:] {
			if a.id == b.id || a.change.File != b.change.File {
				continue
			}
			if !changesConflict(serializer, a.change, b.change) || sharesOwner(a.owners, b.owners) {
				continue
			}
			return conflictError(plans[a.owners[0]].Config, a.change, plans[b.owners[0]].Config, b.change)
		}
	}
	return nil
}

func changesConflict(s *Serializer, a, b types.Change) bool {
	if a.Delete || b.Delete {
		return true
	}
	if a.Start == a.End && b.Start == b.End && a.Start == b.Start {
		return true
	}
	return s.changesOverlap(a, b)
}

func sharesOwner(a, b []int) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func conflictError(cfgA string, a types.Change, cfgB string, b types.Change) error {
	return &types.RefactorError{
		Type: types.InvalidOperation,
		Message: fmt.Sprintf(
			"build configurations disagree on %s: %s proposes %q %s, %s proposes %q %s",
			a.File, cfgA, a.Description, describeSpan(a), cfgB, b.Description, describeSpan(b)),
		File: a.File,
	}
}

func describeSpan(c types.Change) string {
	if c.Delete {
		return "[delete file]"
	}
	return fmt.Sprintf("[%d,%d)", c.Start, c.End)
}
