package connector

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/purpshell/meowcaller"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

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

var cmdCallVideoSelect = &commands.FullHandler{
	Func: fnCallVideoSelect,
	Name: "call-video-select",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Select which WhatsApp group participant is shown on the Matrix camera track.",
		Args:        "<phone number or JID>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallLinkCreate = &commands.FullHandler{
	Func: fnCallLinkCreate,
	Name: "call-link-create",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Create a reusable WhatsApp call link.",
		Args:        "[audio|video]",
	},
	RequiresLogin: true,
}

var cmdCallLinkPreview = &commands.FullHandler{
	Func: fnCallLinkPreview,
	Name: "call-link-preview",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Preview a WhatsApp call link without joining it.",
		Args:        "<link or token> [audio|video]",
	},
	RequiresLogin: true,
}

var cmdCallLinkJoin = &commands.FullHandler{
	Func: fnCallLinkJoin,
	Name: "call-link-join",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Join a WhatsApp call link and ring it into the current Matrix room.",
		Args:        "<link or token> [audio|video]",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallWaiting = &commands.FullHandler{
	Func: fnCallWaiting,
	Name: "call-waiting",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Show the waiting room for the active WhatsApp call link.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallApproval = &commands.FullHandler{
	Func: fnCallApproval,
	Name: "call-approval",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Enable or disable approval for the active WhatsApp call link.",
		Args:        "<on|off>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallAdmit = &commands.FullHandler{
	Func: fnCallAdmit,
	Name: "call-admit",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Admit a user from the active WhatsApp call link waiting room.",
		Args:        "<phone number or JID>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdCallDeny = &commands.FullHandler{
	Func: fnCallDeny,
	Name: "call-deny",
	Help: commands.HelpMeta{
		Section:     HelpSectionCalls,
		Description: "Deny a user from the active WhatsApp call link waiting room.",
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

func fnCallVideoSelect(ce *commands.Event) {
	target, ok := callTargetArg(ce)
	if !ok {
		return
	}
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	if err = client.VOIP.SelectVideoParticipant(call.WACallID, target); err != nil {
		ce.Reply("Failed to select the WhatsApp video participant: %v", err)
		return
	}
	ce.Reply("Selected `%s` for the WhatsApp camera track.", target)
}

func fnCallLinkCreate(ce *commands.Event) {
	video, err := callMediaArg(ce.Args)
	if err != nil {
		ce.Reply("Usage: `$cmdprefix call-link-create [audio|video]`")
		return
	}
	client, err := commandWhatsAppClient(ce)
	if err != nil {
		ce.Reply("Failed to resolve the WhatsApp login: %v", err)
		return
	}
	link, err := client.VOIP.CreateCallLink(ce.Ctx, video)
	if err != nil {
		ce.Reply("Failed to create the WhatsApp call link: %v", err)
		return
	}
	ce.Reply("Created a WhatsApp %s call link:\n\n%s", callMediaName(video), link.URL)
}

func fnCallLinkPreview(ce *commands.Event) {
	token, video, err := callLinkArgs(ce.Args)
	if err != nil {
		ce.Reply("Usage: `$cmdprefix call-link-preview <link or token> [audio|video]`")
		return
	}
	client, err := commandWhatsAppClient(ce)
	if err != nil {
		ce.Reply("Failed to resolve the WhatsApp login: %v", err)
		return
	}
	preview, err := client.VOIP.PreviewCallLink(ce.Ctx, token, video)
	if err != nil {
		ce.Reply("Failed to preview the WhatsApp call link: %v", err)
		return
	}
	creator := preview.Creator
	if !preview.CreatorPhoneNumber.IsEmpty() {
		creator = preview.CreatorPhoneNumber
	}
	ce.Reply(
		"**WhatsApp %s call link**\n\nCreator: `%s`\n\nApproval required: **%t**\n\nYou are an admin: **%t**",
		callMediaName(preview.Video), creator, preview.ApprovalRequired, preview.IsAdmin,
	)
}

func fnCallLinkJoin(ce *commands.Event) {
	token, video, err := callLinkArgs(ce.Args)
	if err != nil {
		ce.Reply("Usage: `$cmdprefix call-link-join <link or token> [audio|video]`")
		return
	}
	client, err := commandWhatsAppClient(ce)
	if err != nil {
		ce.Reply("Failed to resolve the WhatsApp login: %v", err)
		return
	}
	call, err := client.joinMatrixRTCCallLink(ce.Ctx, ce.Portal, token, video)
	if err != nil {
		ce.Reply("Failed to join the WhatsApp call link: %v", err)
		return
	}
	if state, ok, _ := client.VOIP.WaitingRoomState(call.ID()); ok && state.InWaitingRoom {
		ce.Reply("Joined the WhatsApp call link waiting room. Element will ring in this room while approval is pending.")
	} else {
		ce.Reply("Joined the WhatsApp call link. Element will ring in this room.")
	}
}

func fnCallWaiting(ce *commands.Event) {
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	state, ok, err := client.VOIP.WaitingRoomState(call.WACallID)
	if err != nil {
		ce.Reply("Failed to read the waiting room: %v", err)
		return
	}
	if !ok {
		ce.Reply("The active call has no WhatsApp call-link waiting-room state.")
		return
	}
	ce.Reply(formatWaitingRoomState(state))
}

func fnCallApproval(ce *commands.Event) {
	if len(ce.Args) != 1 {
		ce.Reply("Usage: `$cmdprefix call-approval <on|off>`")
		return
	}
	enabled, err := parseCallApproval(ce.Args[0])
	if err != nil {
		ce.Reply("Usage: `$cmdprefix call-approval <on|off>`")
		return
	}
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	if err = client.VOIP.SetApprovalRequired(ce.Ctx, call.WACallID, enabled); err != nil {
		ce.Reply("Failed to change call-link approval: %v", err)
		return
	}
	ce.Reply("WhatsApp call-link approval is now **%s**.", map[bool]string{true: "enabled", false: "disabled"}[enabled])
}

func fnCallAdmit(ce *commands.Event) {
	fnCallWaitingParticipant(ce, true)
}

func fnCallDeny(ce *commands.Event) {
	fnCallWaitingParticipant(ce, false)
}

func fnCallWaitingParticipant(ce *commands.Event, admit bool) {
	target, ok := callTargetArg(ce)
	if !ok {
		return
	}
	client, call, err := activePortalCall(ce)
	if err != nil {
		ce.Reply("Failed to find the active call: %v", err)
		return
	}
	if admit {
		err = client.VOIP.AdmitParticipant(ce.Ctx, call.WACallID, target)
	} else {
		err = client.VOIP.DenyParticipant(ce.Ctx, call.WACallID, target)
	}
	if err != nil {
		ce.Reply("Failed to update the waiting-room participant: %v", err)
		return
	}
	action := "Admitted"
	if !admit {
		action = "Denied"
	}
	ce.Reply("%s `%s` in the WhatsApp call-link waiting room.", action, target)
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
	client, err := commandWhatsAppClient(ce)
	if err != nil {
		return nil, nil, err
	}
	calls, err := client.Main.DB.MatrixRTCCall.GetActiveInRoom(ce.Ctx, ce.Portal.MXID)
	if err != nil {
		return nil, nil, fmt.Errorf("query active calls: %w", err)
	}
	call, err := selectActiveCallForLogin(calls, client.UserLogin.ID)
	if err != nil {
		return nil, nil, err
	}
	return client, call, nil
}

func commandWhatsAppClient(ce *commands.Event) (*WhatsAppClient, error) {
	var loginID networkid.UserLoginID
	if ce.Portal != nil {
		loginID = ce.Portal.Receiver
	} else if login := ce.User.GetDefaultLogin(); login != nil {
		loginID = login.ID
	}
	login := ce.Bridge.GetCachedUserLoginByID(loginID)
	if login == nil {
		return nil, errors.New("the WhatsApp login is not available")
	}
	var sender id.UserID
	if ce.User != nil {
		sender = ce.User.MXID
	}
	if !matrixRTCSenderOwnsLogin(sender, login) {
		return nil, errors.New("the WhatsApp login for this portal belongs to another Matrix user")
	}
	client, ok := login.Client.(*WhatsAppClient)
	if !ok || client == nil || !client.IsLoggedIn() {
		return nil, errors.New("the WhatsApp login is not connected")
	}
	if client.VOIP == nil || !client.VOIP.Enabled() {
		return nil, errors.New("WhatsApp calling is not enabled")
	}
	return client, nil
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

func formatWaitingRoomState(state meowcaller.WaitingRoomState) string {
	lines := []string{
		fmt.Sprintf(
			"**WhatsApp call-link waiting room (transaction %d):** approval **%s**, admin **%t**, waiting **%t**",
			state.TransactionID,
			map[bool]string{true: "enabled", false: "disabled"}[state.Enabled],
			state.IsAdmin,
			state.InWaitingRoom,
		),
	}
	users := slices.Clone(state.Users)
	slices.SortFunc(users, func(a, b meowcaller.WaitingRoomUser) int {
		return strings.Compare(a.JID.String(), b.JID.String())
	})
	for _, user := range users {
		identity := user.JID
		if !user.PN.IsEmpty() {
			identity = user.PN
		}
		lines = append(lines, fmt.Sprintf("- `%s`: %s", identity, user.State))
	}
	if len(users) == 0 {
		lines = append(lines, "- No users are waiting.")
	}
	return strings.Join(lines, "\n")
}

func callLinkArgs(args []string) (token string, video bool, err error) {
	if len(args) < 1 || len(args) > 2 {
		return "", false, errors.New("invalid call-link arguments")
	}
	token = strings.TrimSpace(args[0])
	if token == "" {
		return "", false, errors.New("call-link token is empty")
	}
	if len(args) == 2 {
		video, err = callMediaArg(args[1:])
		return
	}
	video = strings.HasPrefix(strings.ToLower(token), "https://call.whatsapp.com/video/")
	return
}

func callMediaArg(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) != 1 {
		return false, errors.New("invalid call media")
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "audio":
		return false, nil
	case "video":
		return true, nil
	default:
		return false, errors.New("invalid call media")
	}
}

func callMediaName(video bool) string {
	if video {
		return "video"
	}
	return "audio"
}

func parseCallApproval(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "enable", "enabled":
		return true, nil
	case "off", "false", "disable", "disabled":
		return false, nil
	default:
		return false, errors.New("invalid approval state")
	}
}
