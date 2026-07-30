package connector

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/purpshell/meowcaller"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
)

var HelpSectionCalls = commands.HelpSection{Name: "Calls", Order: 27}

var cmdCallParticipants = &commands.FullHandler{
	Func: fnCallParticipants,
	Name: "call-participants",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "List the WhatsApp participants in the active call.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallAdd = &commands.FullHandler{
	Func: fnCallAdd,
	Name: "call-add",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Invite a WhatsApp user to the active call.",
		Args:        "<phone number or JID>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallRing = &commands.FullHandler{
	Func: fnCallRing,
	Name: "call-ring",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Ring a non-connected WhatsApp participant already in the active call.",
		Args:        "<phone number or JID>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

func fnCallParticipants(ce *commands.Event) {
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	state, ok, err := client.VOIP.GroupState(call.WACallID)
	if err != nil {
		ce.Reply("Failed to read the active call roster: %v", err)
		return
	}
	if !ok {
		ce.Reply("WhatsApp has not advertised a group roster for this call yet.")
		return
	}
	ce.Reply(formatGroupCallRoster(state))
}

func fnCallAdd(ce *commands.Event) {
	target, ok := callTargetArg(ce)
	if !ok {
		return
	}
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	if err = client.VOIP.AddParticipant(ce.Ctx, call.WACallID, target); err != nil {
		ce.Reply("Failed to invite the participant: %v", err)
		return
	}
	ce.Reply("Invited `%s` to the active WhatsApp call.", target)
}

func fnCallRing(ce *commands.Event) {
	target, ok := callTargetArg(ce)
	if !ok {
		return
	}
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	if err = client.VOIP.RingParticipant(ce.Ctx, call.WACallID, target); err != nil {
		ce.Reply("Failed to ring the participant: %v", err)
		return
	}
	ce.Reply("Rang `%s` in the active WhatsApp call.", target)
}

func callTargetArg(ce *commands.Event) (string, bool) {
	if len(ce.Args) != 1 {
		ce.Reply("Usage: `$cmdprefix %s <phone number or JID>`", ce.Command)
		return "", false
	}
	return strings.TrimSpace(ce.Args[0]), true
}

func activePortalCall(ce *commands.Event) (*WhatsAppClient, *wadb.MatrixRTCCall, error) {
	if ce.Portal == nil {
		return nil, nil, errors.New("this command can only be used in a portal room")
	}
	login := ce.Bridge.GetCachedUserLoginByID(ce.Portal.Receiver)
	if login == nil {
		return nil, nil, errors.New("the WhatsApp login for this portal is not available")
	}
	client, ok := login.Client.(*WhatsAppClient)
	if !ok || client == nil || !client.IsLoggedIn() {
		return nil, nil, errors.New("the WhatsApp login for this portal is not connected")
	}
	calls, err := client.Main.DB.MatrixRTCCall.GetActiveInRoom(ce.Ctx, ce.Portal.MXID)
	if err != nil {
		return nil, nil, fmt.Errorf("query active calls: %w", err)
	}
	call, err := selectActiveCallForLogin(calls, login.ID)
	if err != nil {
		return nil, nil, err
	}
	return client, call, nil
}

func selectActiveCallForLogin(calls []*wadb.MatrixRTCCall, loginID networkid.UserLoginID) (*wadb.MatrixRTCCall, error) {
	var selected *wadb.MatrixRTCCall
	for _, call := range calls {
		if call == nil || call.UserLoginID != loginID {
			continue
		}
		if selected != nil {
			return nil, errors.New("multiple active calls are tracked in this room")
		}
		selected = call
	}
	if selected == nil {
		return nil, errors.New("there is no active call in this room")
	}
	return selected, nil
}

func formatGroupCallRoster(state meowcaller.GroupCallState) string {
	participants := slices.Clone(state.Participants)
	slices.SortFunc(participants, func(a, b meowcaller.GroupCallParticipant) int {
		return strings.Compare(a.JID.String(), b.JID.String())
	})
	lines := make([]string, 0, len(participants)+1)
	lines = append(lines, fmt.Sprintf("**WhatsApp call participants (transaction %d):**", state.TransactionID))
	for _, participant := range participants {
		identity := participant.JID
		if !participant.PN.IsEmpty() {
			identity = participant.PN
		}
		detail := fmt.Sprintf("%s; %d device(s)", participant.State, len(participant.Devices))
		if participant.HandRaised {
			detail += "; hand raised"
		}
		lines = append(lines, fmt.Sprintf("- `%s`: %s", identity, detail))
	}
	if state.RekeyRequested {
		lines = append(lines, "- WhatsApp requested a group media rekey.")
	}
	return strings.Join(lines, "\n")
}
