package caller2

import (
	"context"

	"tests/thread_context_stops/target"
)

// WithC has a context.Context in scope under a different name, proving the
// threaded expression is the real parameter name and not a hardcoded "ctx".
func WithC(c context.Context) {
	target.Do()
}
