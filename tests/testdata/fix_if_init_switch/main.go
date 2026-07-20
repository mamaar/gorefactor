package main

import "fmt"

func compute(n int) int { return n * 2 }

// classify: switch v := f(); v { ... }
func classify(n int) int {
	switch v := compute(n); v {
	case 0:
		return 0
	case 1:
		return 1
	default:
		return v
	}
}

// describe: switch v := f(); { ... } (nil tag)
func describe(n int) string {
	switch v := compute(n); {
	case v < 0:
		return "negative"
	case v == 0:
		return "zero"
	default:
		return "positive"
	}
}

// typeSwitchDescribe: switch v := f(); y := x.(type) { ... }
func typeSwitchDescribe(n int) string {
	switch v := compute(n); y := interface{}(v).(type) {
	case int:
		return fmt.Sprintf("int:%d", y)
	default:
		return "other"
	}
}

func main() {
	fmt.Println(classify(1), describe(2), typeSwitchDescribe(3))
}
