package refactor

import (
	"reflect"
	"testing"
)

func TestMergeCycles(t *testing.T) {
	configs := []string{"linux", "windows", "darwin"}
	abc := []string{"a", "b", "c", "a"}
	ac := []string{"a", "c", "a"}
	cases := []struct {
		name    string
		perView [][]groupedCycle
		want    []ImportCycle
	}{
		{
			name: "one group with different paths is one entry showing the first path",
			perView: [][]groupedCycle{
				{{abc, "abc"}},
				{{ac, "abc"}},
				nil,
			},
			want: []ImportCycle{{Packages: abc, Configs: []string{"linux", "windows"}}},
		},
		{
			name:    "group cyclic in every view names no configs",
			perView: [][]groupedCycle{{{ac, "ac"}}, {{ac, "ac"}}, {{ac, "ac"}}},
			want:    []ImportCycle{{Packages: ac}},
		},
		{
			name:    "distinct groups stay apart and sort by path",
			perView: [][]groupedCycle{{{[]string{"c", "d", "c"}, "cd"}}, {{[]string{"a", "b", "a"}, "ab"}}, nil},
			want: []ImportCycle{
				{Packages: []string{"a", "b", "a"}, Configs: []string{"windows"}},
				{Packages: []string{"c", "d", "c"}, Configs: []string{"linux"}},
			},
		},
		{
			name:    "no cycles gives an empty non-nil list",
			perView: [][]groupedCycle{nil, nil, nil},
			want:    []ImportCycle{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeCycles(configs, tc.perView)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeCycles = %+v, want %+v", got, tc.want)
			}
		})
	}
}
