package main

import "fmt"

func process(x int) string {
	if x > 0 {
		if x < 100 {
			return fmt.Sprintf("valid: %d", x)
		} else {
			return "too large"
		}
	} else {
		return "negative"
	}
}

func main() {
	fmt.Println(process(42))
}
