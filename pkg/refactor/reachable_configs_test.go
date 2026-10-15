package refactor

import (
	"strings"
	"testing"
)

func reachableFiles() map[string]string {
	return map[string]string{
		"main.go":          "package main\n\nimport \"example.com/threadtest/p\"\n\nfunc main() { p.Run() }\n",
		"p/p.go":           "package p\n\nfunc Run() { run() }\n",
		"p/log.go":         "package p\n\nfunc Log() {}\n\nfunc Always() { Log() }\n\nfunc init() { Always() }\n",
		"p/run_default.go": "//go:build !tagx\n\npackage p\n\nfunc run() {}\n",
		"p/run_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc run() { Log() }\n",
	}
}

func TestReachableAcrossConfigs_TaggedSiteIsReportedOnceWithItsConfig(t *testing.T) {
	eng, _ := loadConfigsEngine(t, reachableFiles())

	report, err := eng.ReachableAcrossConfigs(ReachableRequest{Entry: "example.com/threadtest", Targets: []string{"example.com/threadtest/p.Log"}})

	if err != nil {
		t.Fatal(err)
	}
	var tagged, always int
	for _, s := range report.Sites {
		switch {
		case strings.HasSuffix(s.File, "run_tagx.go"):
			tagged++
			if len(s.Configs) != 1 || !strings.Contains(s.Configs[0], "tagx") {
				t.Errorf("tagged site configs = %v, want one config naming tagx", s.Configs)
			}
		case strings.HasSuffix(s.File, "log.go"):
			always++
			if len(s.Configs) != 0 {
				t.Errorf("shared site configs = %v, want none", s.Configs)
			}
		}
	}
	if tagged != 1 || always != 1 || len(report.Sites) != 2 {
		t.Errorf("sites = %+v, want one tagged and one shared", report.Sites)
	}
}

func TestReachableAcrossConfigs_OverlappingSpecsKeepTheTaggedSiteTagged(t *testing.T) {
	eng, _ := loadConfigsEngine(t, reachableFiles())

	report, err := eng.ReachableAcrossConfigs(ReachableRequest{Entry: "example.com/threadtest", Targets: []string{"example.com/threadtest/p", "example.com/threadtest/p.Log"}})

	if err != nil {
		t.Fatal(err)
	}
	for _, s := range report.Sites {
		if strings.HasSuffix(s.File, "run_tagx.go") && len(s.Configs) != 1 {
			t.Errorf("tagged site configs = %v, want one config", s.Configs)
		}
	}
}

func TestReachableAcrossConfigs_RejectsBadInput(t *testing.T) {
	eng, _ := loadConfigsEngine(t, reachableFiles())
	for name, req := range map[string]ReachableRequest{
		"unknown target": {Entry: "example.com/threadtest", Targets: []string{"example.com/threadtest/p.Nope"}},
		"non-main entry": {Entry: "example.com/threadtest/p", Targets: []string{"example.com/threadtest/p.Log"}},
		"empty entry":    {Targets: []string{"example.com/threadtest/p.Log"}},
		"no targets":     {Entry: "example.com/threadtest"},
		"missing entry":  {Entry: "example.com/threadtest/zzz", Targets: []string{"example.com/threadtest/p.Log"}},
	} {
		if _, err := eng.ReachableAcrossConfigs(req); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
