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
		_, err := eng.ReachableAcrossConfigs(req)
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReachableAcrossConfigs_BodilessPackageIsReportedWithItsConfig(t *testing.T) {
	files := reachableFiles()
	files["p/run_tagx.go"] = "//go:build tagx\n\npackage p\n\n// int f() { return 1; }\nimport \"C\"\n\nfunc run() { Log(); _ = C.f() }\n"
	eng, _ := loadConfigsEngine(t, files)

	report, err := eng.ReachableAcrossConfigs(ReachableRequest{Entry: "example.com/threadtest", Targets: []string{"example.com/threadtest/p.Log"}})

	if err != nil {
		t.Fatal(err)
	}
	if len(report.Bodiless) != 1 || report.Bodiless[0].Package != "example.com/threadtest/p" ||
		len(report.Bodiless[0].Configs) != 1 || !strings.Contains(report.Bodiless[0].Configs[0], "tagx") {
		t.Errorf("bodiless = %+v, want p, in the tagx configuration only", report.Bodiless)
	}
}

func TestReachableAcrossConfigs_EntryBodilessInOneConfigIsReportedNotFatal(t *testing.T) {
	files := reachableFiles()
	files["main_tagx.go"] = "//go:build tagx\n\npackage main\n\n// int f() { return 1; }\nimport \"C\"\n\nvar _ = C.f\n"
	eng, _ := loadConfigsEngine(t, files)

	report, err := eng.ReachableAcrossConfigs(ReachableRequest{Entry: "example.com/threadtest", Targets: []string{"example.com/threadtest/p.Log"}})

	if err != nil {
		t.Fatal(err)
	}
	if len(report.Bodiless) != 1 || report.Bodiless[0].Package != "example.com/threadtest" ||
		len(report.Bodiless[0].Configs) != 1 || !strings.Contains(report.Bodiless[0].Configs[0], "tagx") {
		t.Errorf("bodiless = %+v, want the entry package in the tagx configuration only", report.Bodiless)
	}
	if len(report.Sites) != 1 {
		t.Errorf("sites = %+v, want the one the other configuration finds", report.Sites)
	}
}

func TestReachableAcrossConfigs_EntryBodilessInEveryConfigIsAnError(t *testing.T) {
	files := reachableFiles()
	files["main_cgo.go"] = "package main\n\n// int f() { return 1; }\nimport \"C\"\n\nvar _ = C.f\n"
	eng, _ := loadConfigsEngine(t, files)

	_, err := eng.ReachableAcrossConfigs(ReachableRequest{Entry: "example.com/threadtest", Targets: []string{"example.com/threadtest/p.Log"}})

	if err == nil || !strings.Contains(err.Error(), "cannot be analysed") {
		t.Errorf("err = %v, want one saying the entry package cannot be analysed", err)
	}
}
