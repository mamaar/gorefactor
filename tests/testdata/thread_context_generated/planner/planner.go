package planner

import "tests/thread_context_generated/search"

func Plan(query string) string {
	return search.MapSearch(query)
}
