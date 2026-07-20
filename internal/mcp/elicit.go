package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// errElicitUnsupported is returned by confirm when the connected client did
// not advertise the elicitation capability during initialization. Callers
// must use errors.Is(err, errElicitUnsupported) to tell "could not ask the
// user" apart from "the user said no" (which is a normal (false, nil)
// result), so they can fall back to a safe default instead of failing the
// whole tool call.
var errElicitUnsupported = errors.New("client does not support elicitation")

// confirm asks the connected human user a yes/no question via MCP
// elicitation and reports their answer.
//
// It returns:
//   - (true, nil) if the user accepted,
//   - (false, nil) if the user declined or cancelled,
//   - (false, err) with errors.Is(err, errElicitUnsupported) if the
//     connected client does not support elicitation at all,
//   - (false, err) for any other transport/protocol error.
func confirm(ctx context.Context, sess *mcpsdk.ServerSession, message string) (bool, error) {
	if sess == nil {
		return false, errElicitUnsupported
	}

	// (*mcpsdk.ServerSession).Elicit performs this exact check internally
	// before sending anything over the wire (see the SDK's server.go,
	// around the top of Elicit), and returns an untyped error when it
	// fails. We duplicate the check here using the exported
	// InitializeParams()/Capabilities fields so that "the client never
	// advertised elicitation" is detected structurally - via
	// errors.Is(err, errElicitUnsupported) - rather than by matching on
	// Elicit's error string.
	if iparams := sess.InitializeParams(); iparams == nil || iparams.Capabilities == nil || iparams.Capabilities.Elicitation == nil {
		return false, errElicitUnsupported
	}

	res, err := sess.Elicit(ctx, &mcpsdk.ElicitParams{
		Message: message,
		// "confirm" is intentionally NOT marked required: the SDK
		// server-side validates an "accept" result's Content against this
		// schema before Elicit ever returns to us (see the SDK's
		// server.go, at the end of Elicit), so a required field would
		// turn a missing key into a hard protocol error instead of
		// reaching the tolerant handling below.
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{
					"type":        "boolean",
					"description": "true to confirm, false to cancel",
				},
			},
		},
	})
	if err != nil {
		// The capability check above already covers the common case
		// (client never advertised elicitation at all). Elicit can still
		// fail with a similarly-worded "unsupported" error for
		// mode-specific reasons (e.g. a client that only advertises URL
		// elicitation, so "form" mode is rejected) - the SDK does not
		// export a sentinel or typed error for that, so a string match
		// isolated in isElicitUnsupportedErr is the only way to catch it.
		if isElicitUnsupportedErr(err) {
			return false, fmt.Errorf("%w: %v", errElicitUnsupported, err)
		}
		return false, err
	}

	switch res.Action {
	case "accept":
		// Be tolerant: a missing "confirm" key on accept counts as true.
		// Only an explicit false in the response counts as a decline.
		if v, ok := res.Content["confirm"]; ok {
			if b, ok := v.(bool); ok && !b {
				return false, nil
			}
		}
		return true, nil
	default: // "decline", "cancel", or anything unrecognized.
		return false, nil
	}
}

// isElicitUnsupportedErr reports whether err is one of the "client does not
// support ... elicitation" errors that (*mcpsdk.ServerSession).Elicit
// returns when the client's advertised capabilities don't cover the
// requested elicitation mode. The SDK builds these with fmt.Errorf/
// errors.New directly and exposes no sentinel, so string matching is
// genuinely the only option; this function exists to keep that fragility
// contained to one place.
func isElicitUnsupportedErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "does not support") && strings.Contains(msg, "elicitation")
}
