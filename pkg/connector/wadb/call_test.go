package wadb

import (
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestMatrixRTCCallSQLVariablesIncludeReactionEventIDs(t *testing.T) {
	call := &MatrixRTCCall{
		BridgeMembershipEventID:   id.EventID("$bridge-member"),
		SelectedMembershipEventID: id.EventID("$selected-member"),
		BridgeHandRaiseEventID:    id.EventID("$bridge-hand"),
		SelectedHandRaiseEventID:  id.EventID("$selected-hand"),
	}
	variables := call.sqlVariables()
	if len(variables) != 27 {
		t.Fatalf("MatrixRTCCall.sqlVariables returned %d values, want 27", len(variables))
	}
	for index, want := range map[int]string{
		15: "$bridge-member",
		16: "$selected-member",
		17: "$bridge-hand",
		18: "$selected-hand",
	} {
		value, ok := variables[index].(*string)
		if !ok || value == nil || *value != want {
			t.Fatalf("SQL variable %d = %#v, want %q", index, variables[index], want)
		}
	}
}
