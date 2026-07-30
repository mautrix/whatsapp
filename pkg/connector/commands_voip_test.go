package connector

import (
	"strings"
	"testing"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
)

func TestSelectActiveCallForLogin(t *testing.T) {
	alice := networkid.UserLoginID("alice")
	bob := networkid.UserLoginID("bob")
	calls := []*wadb.MatrixRTCCall{
		{UserLoginID: bob, WACallID: "bob-call"},
		{UserLoginID: alice, WACallID: "alice-call"},
	}
	call, err := selectActiveCallForLogin(calls, alice)
	if err != nil {
		t.Fatalf("selectActiveCallForLogin returned error: %v", err)
	}
	if call.WACallID != "alice-call" {
		t.Fatalf("selected call = %q, want alice-call", call.WACallID)
	}
}

func TestSelectActiveCallForLoginRejectsMissingAndAmbiguousCalls(t *testing.T) {
	loginID := networkid.UserLoginID("alice")
	if _, err := selectActiveCallForLogin(nil, loginID); err == nil {
		t.Fatal("selectActiveCallForLogin accepted an empty call list")
	}
	calls := []*wadb.MatrixRTCCall{
		{UserLoginID: loginID, WACallID: "first"},
		{UserLoginID: loginID, WACallID: "second"},
	}
	if _, err := selectActiveCallForLogin(calls, loginID); err == nil {
		t.Fatal("selectActiveCallForLogin accepted multiple calls for one login")
	}
}

func TestFormatGroupCallRoster(t *testing.T) {
	state := meowcaller.GroupCallState{
		TransactionID:  42,
		RekeyRequested: true,
		Participants: []meowcaller.GroupCallParticipant{
			{
				JID:   types.NewJID("222", types.HiddenUserServer),
				PN:    types.NewJID("15550000002", types.DefaultUserServer),
				State: "connected",
				Devices: []meowcaller.GroupCallDevice{
					{JID: types.NewJID("222", types.HiddenUserServer)},
				},
				HandRaised: true,
			},
			{
				JID:   types.NewJID("111", types.HiddenUserServer),
				State: "ringing",
			},
		},
	}
	got := formatGroupCallRoster(state)
	for _, want := range []string{
		"transaction 42",
		"`111@lid`: ringing; 0 device(s)",
		"`15550000002@s.whatsapp.net`: connected; 1 device(s); hand raised",
		"requested a group media rekey",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted roster missing %q:\n%s", want, got)
		}
	}
}
