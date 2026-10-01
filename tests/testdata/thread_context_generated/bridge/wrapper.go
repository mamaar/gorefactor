package bridge

import "tests/thread_context_generated/search"

type NativeBridgeWrapper struct{}

func (w *NativeBridgeWrapper) MapSearch(query string) string {
	return search.MapSearch(query)
}
