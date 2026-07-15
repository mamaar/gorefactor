package caller3

import (
	"net/http"

	"tests/thread_context_stops/target"
)

// H has no context.Context in scope but does have an *http.Request, whose
// .Context() stands in for one.
func H(w http.ResponseWriter, r *http.Request) {
	target.Do()
}
