package main

import (
	"fmt"

	"tests/thread_context_generated/bridge"
	"tests/thread_context_generated/planner"
)

func main() {
	fmt.Println(planner.Plan("route"))
	adapter := bridge.NewNativeBridgeAdapter(&bridge.NativeBridgeWrapper{})
	fmt.Println(adapter.Search("route"))
}
