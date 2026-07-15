package main

import "net/http"

// H.ServeHTTP satisfies the externally-declared net/http.Handler interface.
// Threading it would silently break that satisfaction, which cannot be
// repaired by rewriting anything this workspace owns, so it must abort
// instead.
type H struct{}

func (h *H) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

var _ http.Handler = &H{}

func main() {}
