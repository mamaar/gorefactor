package main

import (
	"fmt"
	"strconv"
)

func parseAndPrint(s string) {
	if val, err := strconv.Atoi(s); err != nil {
		fmt.Println("error:", err)
	} else {
		fmt.Println("value:", val)
	}
}

func main() {
	parseAndPrint("42")
	parseAndPrint("abc")
}
