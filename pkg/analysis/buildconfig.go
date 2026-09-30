package analysis

import (
	"bytes"
	"go/build"
	"io"
	"slices"
	"strconv"
	"strings"
)

// BuildConfig is one build configuration that file selection is evaluated under.
type BuildConfig struct {
	GOOS   string
	GOARCH string
	Cgo    bool
	Tags   []string
}

// NewBuildConfig returns a BuildConfig with tags sorted and deduplicated.
func NewBuildConfig(goos, goarch string, cgo bool, tags ...string) BuildConfig {
	return BuildConfig{GOOS: goos, GOARCH: goarch, Cgo: cgo, Tags: normalizeTags(tags)}
}

// DefaultBuildConfig returns the host configuration from go/build.Default.
func DefaultBuildConfig() BuildConfig {
	d := build.Default
	return NewBuildConfig(d.GOOS, d.GOARCH, d.CgoEnabled, d.BuildTags...)
}

func normalizeTags(tags []string) []string {
	out := slices.Clone(tags)
	slices.Sort(out)
	return slices.Compact(out)
}

// Key returns a stable identity string such as "darwin/arm64/cgo/fts5,gcp".
func (c BuildConfig) Key() string {
	cgo := "nocgo"
	if c.Cgo {
		cgo = "cgo"
	}
	return strings.Join([]string{c.GOOS, c.GOARCH, cgo, strings.Join(normalizeTags(c.Tags), ",")}, "/")
}

// String describes the config as the delta from DefaultBuildConfig, or "default".
func (c BuildConfig) String() string {
	def := DefaultBuildConfig()
	var parts []string
	if c.GOOS != def.GOOS {
		parts = append(parts, "GOOS="+c.GOOS)
	}
	if c.GOARCH != def.GOARCH {
		parts = append(parts, "GOARCH="+c.GOARCH)
	}
	if c.Cgo != def.Cgo {
		parts = append(parts, "cgo="+onOff(c.Cgo))
	}
	if tags := normalizeTags(c.Tags); !slices.Equal(tags, def.Tags) {
		parts = append(parts, "tags="+strings.Join(tags, ","))
	}
	if len(parts) == 0 {
		return "default"
	}
	return strings.Join(parts, " ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// MatchesFile reports whether f is part of a package under this config:
// build constraints and filename suffixes are satisfied, and a file importing
// "C" requires Cgo.
func (c BuildConfig) MatchesFile(f *SourceFile) bool {
	ctx := c.context()
	ctx.OpenFile = contentOpener{content: f.Content}.open
	ok, err := ctx.MatchFile(f.Dir, f.Name)
	if err != nil || !ok {
		return false
	}
	return c.Cgo || !importsC(f)
}

func (c BuildConfig) context() build.Context {
	d := build.Default
	return build.Context{
		GOOS:        c.GOOS,
		GOARCH:      c.GOARCH,
		CgoEnabled:  c.Cgo,
		BuildTags:   normalizeTags(c.Tags),
		ReleaseTags: d.ReleaseTags,
		ToolTags:    d.ToolTags,
		Compiler:    d.Compiler,
	}
}

func importsC(f *SourceFile) bool {
	if f.AST == nil {
		return false
	}
	for _, imp := range f.AST.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "C" {
			return true
		}
	}
	return false
}

// contentOpener serves already-read bytes to go/build in place of a disk read.
type contentOpener struct{ content []byte }

func (o contentOpener) open(string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.content)), nil
}
