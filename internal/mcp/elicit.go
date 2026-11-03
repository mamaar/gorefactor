package mcp

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// canConfirm reports whether the connected client can show a form dialog.
// Advertising elicitation with neither mode set means form, as in the SDK.
func canConfirm(sess *mcpsdk.ServerSession) bool {
	iparams := sess.InitializeParams()
	if iparams == nil || iparams.Capabilities == nil || iparams.Capabilities.Elicitation == nil {
		return false
	}

	caps := iparams.Capabilities.Elicitation

	return caps.Form != nil || caps.URL == nil
}

// confirmRequest builds a yes/no question for the connected human user, to be
// returned as an input request from a tool handler.
func confirmRequest(message string) *mcpsdk.ElicitParams {
	return &mcpsdk.ElicitParams{
		Message: message,
		// "confirm" is not required: a client may accept without sending it,
		// and on older protocols the SDK validates an accepted result against
		// this schema, so requiring it would turn that into a protocol error.
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{
					"type":        "boolean",
					"description": "true to confirm, false to cancel",
				},
			},
		},
	}
}

// confirmAccepted reports whether answer accepts a confirmRequest. A missing
// "confirm" key on accept counts as yes; any value but true is a no, because
// nothing validates the answer against the schema on current protocols.
func confirmAccepted(answer mcpsdk.InputResponse) bool {
	res, ok := answer.(*mcpsdk.ElicitResult)
	if !ok || res.Action != "accept" {
		return false
	}

	v, present := res.Content["confirm"]

	return !present || v == true
}
