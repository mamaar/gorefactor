package main

import "fmt"

// guard_clause: a trailing if in a function with no results.
func report(verbose bool, name string) {
	if verbose {
		// keep this comment attached to the body
		prefix := "report"
		detail := fmt.Sprintf("%s: %s", prefix, name)
		fmt.Println(detail)
	}
}

// loop_continue: a trailing if in a range body.
func summarize(values []int) {
	for _, value := range values {
		if value > 0 {
			doubled := value * 2
			label := fmt.Sprintf("value %d", doubled)
			fmt.Println(label)
		}
	}
}

// else_after_return: the if branch already returns, so the else is redundant.
func classify(x int) string {
	if x < 0 {
		return "negative"
	} else {
		kind := "non-negative"
		return kind
	}
}

// Unfixable: dedenting the body would redeclare limit alongside the outer one,
// which does not compile. The tool must leave this function untouched.
func clamp(value int) {
	limit := 10
	fmt.Println(limit)
	if value > limit {
		limit := value
		adjusted := limit - 1
		fmt.Println(adjusted)
	}
}

func main() {
	report(true, "example")
	summarize([]int{1, -2, 3})
	fmt.Println(classify(-1))
	clamp(20)
}
