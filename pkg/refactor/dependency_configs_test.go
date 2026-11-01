package refactor

import (
	"reflect"
	"testing"
)

func TestMergeCycles(t *testing.T) {
	configs := []string{"linux", "windows", "darwin"}
	cases := []struct {
		name    string
		perView [][][]string
		want    []ImportCycle
	}{
		{
			name:    "same cycle rotated in two views is one entry naming both",
			perView: [][][]string{{{"a", "b", "c", "a"}}, {{"b", "c", "a", "b"}}, nil},
			want:    []ImportCycle{{Packages: []string{"a", "b", "c", "a"}, Configs: []string{"linux", "windows"}}},
		},
		{
			name:    "cycle in every view names no configs",
			perView: [][][]string{{{"a", "b", "a"}}, {{"a", "b", "a"}}, {{"a", "b", "a"}}},
			want:    []ImportCycle{{Packages: []string{"a", "b", "a"}}},
		},
		{
			name:    "distinct cycles stay apart and sort by path",
			perView: [][][]string{{{"c", "d", "c"}}, {{"a", "b", "a"}}, nil},
			want: []ImportCycle{
				{Packages: []string{"a", "b", "a"}, Configs: []string{"windows"}},
				{Packages: []string{"c", "d", "c"}, Configs: []string{"linux"}},
			},
		},
		{
			name:    "no cycles gives an empty non-nil list",
			perView: [][][]string{nil, nil, nil},
			want:    []ImportCycle{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeCycles(configs, tc.perView); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeCycles = %+v, want %+v", got, tc.want)
			}
		})
	}
}
