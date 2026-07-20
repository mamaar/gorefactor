// Package main holds the chain that motivated generalising
// fix_deep_if_else_chains: nesting depth 5, an `else if` at the top, two
// `if x, ok := …; ok` headers, and every single branch already returning.
//
// The old fixer refused it outright. The current one flattens it to depth 2 on
// its own, and all the way to depth 1 once fix_if_init_assignments has hoisted
// the two headers - which is the composition this fixture exists to pin.
package main

import (
	"fmt"
	"slices"
)

type config struct {
	Type        string
	TypeField   string
	TypeMapping map[string]string
	IgnoreTypes []string
}

func findTypeString(c config, attributes map[string]string) (*string, error) {
	if len(c.Type) > 0 {
		return &c.Type, nil
	} else if len(c.TypeField) > 0 {
		if typeKey, ok := attributes[c.TypeField]; ok {
			if typeValue, ok := c.TypeMapping[typeKey]; ok {
				return &typeValue, nil
			} else if slices.Contains(c.IgnoreTypes, typeKey) {
				return nil, nil
			} else {
				return nil, fmt.Errorf("no mapping for type key %q", typeKey)
			}
		} else {
			return nil, fmt.Errorf("attribute %q is missing", c.TypeField)
		}
	} else {
		return nil, fmt.Errorf("no type and no type field configured")
	}
}

func main() {
	c := config{Type: "point"}
	got, err := findTypeString(c, map[string]string{})
	fmt.Println(got, err)
}
