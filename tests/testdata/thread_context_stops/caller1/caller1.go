package caller1

import (
	"context"

	"tests/thread_context_stops/target"
)

// WithCtx already has a context.Context named "ctx" in scope, so the climb
// must stop here and pass that variable's actual name.
func WithCtx(ctx context.Context) {
	target.Do()
}
