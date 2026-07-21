// Package main exercises detect_switchable_if_chains and
// fix_switchable_if_chains across the three switch shapes they recognise, plus
// the two chains that must be left alone.
//
//   - httpStatusMessage is a tagged chain: every link compares the same `code`
//     against one or more values, so it converts to
//     `switch code { case 200, 201: ... }`.
//   - classifyMessage is a tagless chain whose conditions call
//     strings.HasPrefix and len. A tagless switch hoists nothing, so those
//     calls stay exactly as often-evaluated as they were in the chain, and the
//     chain still converts.
//   - describeValue is a type-switch chain: every link is a `v, ok := x.(T);
//     ok` assertion sharing one asserted operand and one binding name.
//   - gradeScore is the control that must be left to fix_deep_if_else_chains
//     instead: every branch returns and no header declares anything, which is
//     exactly the shape flattenerHandlesIt claims for that other fixer. It must
//     not even appear in detect_switchable_if_chains' violations, since this
//     analyzer never reports a chain the flattener already owns.
//   - resolveKey is refused: its second link's
//     `if v, err := fetchRemote(key); err != nil` declares variables in a
//     later-link header, which a switch has nowhere to put (`switch init; tag
//     {` only ever carries the first link's init). It must show up in
//     `unfixable` with a reason naming fix_if_init_assignments.
package main

import (
	"fmt"
	"strings"
)

func httpStatusMessage(code int) string {
	msg := ""
	if code == 200 || code == 201 {
		msg = "OK"
	} else if code == 404 {
		msg = "Not Found"
	} else {
		msg = "Unknown"
	}
	return msg
}

func classifyMessage(s string) string {
	category := ""
	if strings.HasPrefix(s, "ERROR") {
		category = "error"
	} else if len(s) == 0 {
		category = "empty"
	} else if strings.HasPrefix(s, "WARN") {
		category = "warning"
	} else {
		category = "info"
	}
	return category
}

func describeValue(x any) string {
	if v, ok := x.(int); ok {
		return fmt.Sprintf("int:%d", v)
	} else if v, ok := x.(string); ok {
		return fmt.Sprintf("string:%s", v)
	} else if v, ok := x.(bool); ok {
		return fmt.Sprintf("bool:%v", v)
	} else {
		return "unknown"
	}
}

func gradeScore(score int) string {
	if score >= 90 {
		return "A"
	} else if score >= 80 {
		return "B"
	} else if score >= 70 {
		return "C"
	} else {
		return "F"
	}
}

func fetchRemote(key string) (string, error) {
	if key == "missing" {
		return "", fmt.Errorf("not found: %s", key)
	}
	return "value-" + key, nil
}

func resolveKey(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty key")
	} else if v, err := fetchRemote(key); err != nil {
		return "", err
	} else {
		return v, nil
	}
}

func main() {
	fmt.Println(httpStatusMessage(404))
	fmt.Println(classifyMessage("WARN: disk low"))
	fmt.Println(describeValue(42))
	fmt.Println(gradeScore(85))
	v, err := resolveKey("abc")
	fmt.Println(v, err)
}
