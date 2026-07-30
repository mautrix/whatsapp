package connector

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/voip"
	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
)

func TestShouldStartOutboundMatrixRTCCall(t *testing.T) {
	evt := matrixRTCMemberEvent(event.StateEventType)
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if !shouldStartOutboundMatrixRTCCall(evt, parsed, "auto") {
		t.Fatalf("shouldStartOutboundMatrixRTCCall returned false for active state membership")
	}
}

func TestMatrixRTCSenderMustOwnPortalLogin(t *testing.T) {
	login := &bridgev2.UserLogin{
		User: &bridgev2.User{
			User: &database.User{MXID: "@alice:example.com"},
		},
	}
	if !matrixRTCSenderOwnsLogin("@alice:example.com", login) {
		t.Fatal("matrixRTCSenderOwnsLogin rejected the login owner")
	}
	if matrixRTCSenderOwnsLogin("@mallory:example.com", login) {
		t.Fatal("matrixRTCSenderOwnsLogin accepted another Matrix user")
	}
	if matrixRTCSenderOwnsLogin("@alice:example.com", nil) {
		t.Fatal("matrixRTCSenderOwnsLogin accepted a missing login")
	}
}

func TestIncomingGroupCallUsesGroupPortalAndConsumesCorrelation(t *testing.T) {
	client := &WhatsAppClient{incomingCallGroups: make(map[string]incomingCallGroup)}
	group := types.NewJID("120363000000000000", types.GroupServer)
	fallback := types.NewJID("15550000001", types.DefaultUserServer)

	client.trackIncomingCallGroup("CALL", group)
	if got := client.incomingCallPortalPeer("CALL", fallback); got != group {
		t.Fatalf("incoming portal peer = %s, want group %s", got, group)
	}
	if got := client.incomingCallPortalPeer("CALL", fallback); got != fallback {
		t.Fatalf("consumed incoming portal peer = %s, want fallback %s", got, fallback)
	}
}

func TestIncomingCallEventTracksOfferGroupBeforeManagedCall(t *testing.T) {
	client := &WhatsAppClient{incomingCallGroups: make(map[string]incomingCallGroup)}
	group := types.NewJID("120363000000000000", types.GroupServer)
	client.trackIncomingCallEvent(&events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			CallID:   "CALL",
			GroupJID: group,
		},
	})
	if got := client.incomingCallPortalPeer("CALL", types.EmptyJID); got != group {
		t.Fatalf("incoming portal peer = %s, want group %s", got, group)
	}
}

func TestIncomingCallGroupIgnoresNonGroupJID(t *testing.T) {
	client := &WhatsAppClient{incomingCallGroups: make(map[string]incomingCallGroup)}
	direct := types.NewJID("15550000001", types.DefaultUserServer)
	client.trackIncomingCallGroup("CALL", direct)
	if got := client.incomingCallPortalPeer("CALL", direct); got != direct {
		t.Fatalf("incoming portal peer = %s, want direct fallback %s", got, direct)
	}
	if len(client.incomingCallGroups) != 0 {
		t.Fatalf("tracked non-group calls = %d, want 0", len(client.incomingCallGroups))
	}
}

func TestIncomingCallGroupIgnoresExpiredCorrelation(t *testing.T) {
	group := types.NewJID("120363000000000000", types.GroupServer)
	fallback := types.NewJID("15550000001", types.DefaultUserServer)
	client := &WhatsAppClient{incomingCallGroups: map[string]incomingCallGroup{
		"CALL": {
			JID:       group,
			ExpiresAt: time.Now().Add(-time.Second),
		},
	}}
	if got := client.incomingCallPortalPeer("CALL", fallback); got != fallback {
		t.Fatalf("expired incoming portal peer = %s, want fallback %s", got, fallback)
	}
}

func TestShouldStartOutboundMatrixRTCCallRejectsMessageMembership(t *testing.T) {
	evt := matrixRTCMemberEvent(event.MessageEventType)
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if shouldStartOutboundMatrixRTCCall(evt, parsed, "auto") {
		t.Fatalf("shouldStartOutboundMatrixRTCCall returned true for message membership")
	}
}

func TestShouldStartOutboundMatrixRTCCallRejectsActivePreviousState(t *testing.T) {
	evt := matrixRTCMemberEvent(event.StateEventType)
	evt.Unsigned.PrevContent = &event.Content{Raw: matrixRTCMemberContent()}
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if shouldStartOutboundMatrixRTCCall(evt, parsed, "auto") {
		t.Fatalf("shouldStartOutboundMatrixRTCCall returned true for an active-to-active state update")
	}
}

func TestMatrixRTCTriggerStateKeyUsesEventStateKey(t *testing.T) {
	evt := matrixRTCMemberEvent(event.StateEventType)
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if stateKey := matrixRTCTriggerStateKey(parsed); stateKey != "@alice:example.com_DEVICE" {
		t.Fatalf("state key = %q, want event state key", stateKey)
	}
}

func TestMatrixRTCTriggerStateKeyFallsBackToSenderAndSession(t *testing.T) {
	parsed := voip.MatrixRTCEvent{
		Sender:    "@alice:example.com",
		SessionID: "SESSION",
	}
	if stateKey := matrixRTCTriggerStateKey(parsed); stateKey != "@alice:example.com_SESSION" {
		t.Fatalf("state key = %q, want sender/session-derived key", stateKey)
	}
}

func TestMatrixRTCTriggerStickyKeyPrefersContentStickyKey(t *testing.T) {
	parsed := voip.MatrixRTCEvent{
		Sender:    "@alice:example.com",
		SessionID: "SESSION",
		Raw: map[string]any{
			"sticky_key": "sticky",
		},
	}
	if stickyKey := matrixRTCTriggerStickyKey(parsed); stickyKey != "sticky" {
		t.Fatalf("sticky key = %q, want content sticky key", stickyKey)
	}
}

func TestMatrixRTCTriggerStickyKeyFallsBackToParticipantID(t *testing.T) {
	parsed := voip.MatrixRTCEvent{
		Sender:   "@alice:example.com",
		DeviceID: "DEVICE",
		Raw:      map[string]any{},
	}
	if stickyKey := matrixRTCTriggerStickyKey(parsed); stickyKey != "@alice:example.com:DEVICE" {
		t.Fatalf("sticky key = %q, want participant id", stickyKey)
	}
}

func TestMatrixRTCOutboundMediaKindDefaultsToAudio(t *testing.T) {
	mediaKind, downgraded := matrixRTCOutboundMediaKind(voip.MatrixRTCEvent{})
	if mediaKind != "audio" {
		t.Fatalf("mediaKind = %q, want audio", mediaKind)
	}
	if downgraded {
		t.Fatalf("downgraded = true, want false")
	}
}

func TestMatrixRTCOutboundMediaKindKeepsAudio(t *testing.T) {
	mediaKind, downgraded := matrixRTCOutboundMediaKind(voip.MatrixRTCEvent{Intent: "audio"})
	if mediaKind != "audio" {
		t.Fatalf("mediaKind = %q, want audio", mediaKind)
	}
	if downgraded {
		t.Fatalf("downgraded = true, want false")
	}
}

func TestMatrixRTCOutboundMediaKindKeepsVideo(t *testing.T) {
	mediaKind, downgraded := matrixRTCOutboundMediaKind(voip.MatrixRTCEvent{Intent: "video"})
	if mediaKind != "video" {
		t.Fatalf("mediaKind = %q, want video", mediaKind)
	}
	if downgraded {
		t.Fatalf("downgraded = true, want false")
	}
}

func TestMatrixRTCOutboundMediaKindRejectsUnknown(t *testing.T) {
	mediaKind, downgraded := matrixRTCOutboundMediaKind(voip.MatrixRTCEvent{Intent: "screen"})
	if mediaKind != "" {
		t.Fatalf("mediaKind = %q, want empty", mediaKind)
	}
	if downgraded {
		t.Fatalf("downgraded = true, want false")
	}
}

func TestShouldEndMatrixRTCCallFromLegacyMembershipLeave(t *testing.T) {
	stateKey := "_@alice:example.com_DEVICE_m.call"
	evt := &event.Event{
		Type:     voip.GroupCallMemberEventType(),
		RoomID:   id.RoomID("!room:example.com"),
		Sender:   id.UserID("@alice:example.com"),
		StateKey: &stateKey,
		Content:  event.Content{Raw: map[string]any{}},
	}
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if !shouldEndMatrixRTCCallFromMembership(parsed, "@alice:example.com:DEVICE") {
		t.Fatalf("shouldEndMatrixRTCCallFromMembership returned false for selected participant leave")
	}
}

func TestShouldEndMatrixRTCCallFromMembershipKeepsActiveJoin(t *testing.T) {
	evt := matrixRTCMemberEvent(event.StateEventType)
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if shouldEndMatrixRTCCallFromMembership(parsed, "@alice:example.com:DEVICE") {
		t.Fatalf("shouldEndMatrixRTCCallFromMembership returned true for active join")
	}
}

func TestShouldEndMatrixRTCCallFromMembershipRejectsOtherParticipant(t *testing.T) {
	stateKey := "_@alice:example.com_OTHER_m.call"
	evt := &event.Event{
		Type:     voip.GroupCallMemberEventType(),
		RoomID:   id.RoomID("!room:example.com"),
		Sender:   id.UserID("@alice:example.com"),
		StateKey: &stateKey,
		Content:  event.Content{Raw: map[string]any{}},
	}
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership")
	}
	if shouldEndMatrixRTCCallFromMembership(parsed, "@alice:example.com:DEVICE") {
		t.Fatalf("shouldEndMatrixRTCCallFromMembership returned true for another participant")
	}
}

func TestMatrixRTCPortalSupportsWhatsAppGroupCalls(t *testing.T) {
	peer := types.NewJID("120363000000000000", types.GroupServer)
	if !matrixRTCPortalSupportsWhatsAppCalls(peer) {
		t.Fatal("matrixRTCPortalSupportsWhatsAppCalls rejected a WhatsApp group portal")
	}
}

func TestMatrixRTCControlEventMatchesSelectedMembership(t *testing.T) {
	call := &wadb.MatrixRTCCall{
		SelectedPublisherID:       "@alice:example.com:DEVICE",
		SelectedMembershipEventID: "$membership",
	}
	reaction := voip.MatrixRTCEvent{
		Kind:             voip.MatrixRTCEventKindCallReaction,
		Sender:           "@alice:example.com",
		RelatesToEventID: "$membership",
	}
	if !matrixRTCControlEventMatchesCall(reaction, call) {
		t.Fatal("reaction did not match its selected MatrixRTC membership")
	}
	reaction.Sender = "@mallory:example.com"
	if matrixRTCControlEventMatchesCall(reaction, call) {
		t.Fatal("reaction from another Matrix user matched the selected membership")
	}
}

func TestWhatsAppRemoteHandRaisesAreAggregated(t *testing.T) {
	wa := &WhatsAppClient{}
	alice := types.NewJID("111", types.DefaultUserServer)
	bob := types.NewJID("222", types.DefaultUserServer)

	raised, changed := wa.updateWhatsAppRemoteHandRaise("call", meowcaller.HandRaiseState{Participant: alice, Raised: true})
	if !raised || !changed {
		t.Fatalf("first raise = %t, %t, want true, true", raised, changed)
	}
	raised, changed = wa.updateWhatsAppRemoteHandRaise("call", meowcaller.HandRaiseState{Participant: bob, Raised: true})
	if !raised || changed {
		t.Fatalf("second raise = %t, %t, want true, false", raised, changed)
	}
	raised, changed = wa.updateWhatsAppRemoteHandRaise("call", meowcaller.HandRaiseState{Participant: alice, Raised: false})
	if !raised || changed {
		t.Fatalf("first lower = %t, %t, want true, false", raised, changed)
	}
	raised, changed = wa.updateWhatsAppRemoteHandRaise("call", meowcaller.HandRaiseState{Participant: bob, Raised: false})
	if raised || !changed {
		t.Fatalf("last lower = %t, %t, want false, true", raised, changed)
	}
}

func TestReserveMatrixRTCOutboundStartSuppressesDuplicates(t *testing.T) {
	wa := &WhatsAppConnector{}
	if !wa.reserveMatrixRTCOutboundStart("!room:example.com") {
		t.Fatalf("first reserve returned false")
	}
	if wa.reserveMatrixRTCOutboundStart("!room:example.com") {
		t.Fatalf("second reserve returned true, want false")
	}
	if !wa.reserveMatrixRTCOutboundStart("!other:example.com") {
		t.Fatalf("reserve for another room returned false")
	}
}

func TestMatrixRTCFinalEndReasonPreservesActivationFailure(t *testing.T) {
	reason, lastErr := matrixRTCFinalEndReason(&wadb.MatrixRTCCall{
		EndedTS:   time.Unix(123, 0),
		EndReason: "livekit_bridge_failed",
		LastError: "could not connect after timeout",
	}, "rejected")
	if reason != "livekit_bridge_failed" {
		t.Fatalf("reason = %q, want livekit_bridge_failed", reason)
	}
	if lastErr != "could not connect after timeout" {
		t.Fatalf("lastErr = %q, want timeout error", lastErr)
	}
}

func TestMatrixRTCFinalEndReasonUsesWhatsAppReasonForFreshEnd(t *testing.T) {
	reason, lastErr := matrixRTCFinalEndReason(&wadb.MatrixRTCCall{}, "rejected")
	if reason != "rejected" {
		t.Fatalf("reason = %q, want rejected", reason)
	}
	if lastErr != "" {
		t.Fatalf("lastErr = %q, want empty", lastErr)
	}
}

func TestMatrixRTCLiveKitAuthRequestUsesStrictJWTFields(t *testing.T) {
	req := matrixRTCLiveKitAuthRequest(&wadb.MatrixRTCCall{
		RoomID:                "!room:example.com",
		MatrixParticipantMXID: "@whatsapp_123:example.com",
		MatrixSessionID:       "WA123",
	}, voip.MatrixOpenIDToken{AccessToken: "openid"})

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	for _, field := range []string{
		`"device_id"`,
		`"session_id"`,
		`"participant_id"`,
		`"focus_type"`,
		`"extra"`,
	} {
		if bytes.Contains(body, []byte(field)) {
			t.Fatalf("request body contains strict-JWT-incompatible field %s: %s", field, string(body))
		}
	}
	if req.RoomID != "!room:example.com" || req.SlotID != voip.MatrixRTCDefaultSlotID {
		t.Fatalf("unexpected room or slot in request: %+v", req)
	}
	if req.Member == nil || req.Member.ID != "@whatsapp_123:example.com:WA123" {
		t.Fatalf("unexpected member in request: %+v", req.Member)
	}
}

func TestMatrixRTCLegacyLiveKitAuthRequestUsesRoomAndDevice(t *testing.T) {
	req := matrixRTCLegacyLiveKitAuthRequest(&wadb.MatrixRTCCall{
		RoomID:          "!room:example.com",
		MatrixSessionID: "WA123",
	}, voip.MatrixOpenIDToken{AccessToken: "openid"})

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if !bytes.Contains(body, []byte(`"room":"!room:example.com"`)) {
		t.Fatalf("request body missing legacy room: %s", string(body))
	}
	if !bytes.Contains(body, []byte(`"device_id":"WA123"`)) {
		t.Fatalf("request body missing legacy device_id: %s", string(body))
	}
	if bytes.Contains(body, []byte(`"member"`)) || bytes.Contains(body, []byte(`"slot_id"`)) {
		t.Fatalf("legacy request body contains modern fields: %s", string(body))
	}
}

func TestMatrixRTCCompatModeConfigValues(t *testing.T) {
	tests := []struct {
		mode       string
		wantModern bool
		wantLegacy bool
	}{
		{mode: "auto", wantModern: true, wantLegacy: true},
		{mode: "msc4143", wantModern: true, wantLegacy: false},
		{mode: "msc3401", wantModern: false, wantLegacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if got := matrixRTCCompatAllowsModern(tt.mode); got != tt.wantModern {
				t.Fatalf("matrixRTCCompatAllowsModern(%q) = %v, want %v", tt.mode, got, tt.wantModern)
			}
			if got := matrixRTCCompatAllowsLegacy(tt.mode); got != tt.wantLegacy {
				t.Fatalf("matrixRTCCompatAllowsLegacy(%q) = %v, want %v", tt.mode, got, tt.wantLegacy)
			}
		})
	}
}

func matrixRTCMemberEvent(class event.TypeClass) *event.Event {
	stateKey := "@alice:example.com_DEVICE"
	return &event.Event{
		Type:     voip.RTCMembershipEventType(class),
		RoomID:   id.RoomID("!room:example.com"),
		Sender:   id.UserID("@alice:example.com"),
		StateKey: &stateKey,
		Content:  event.Content{Raw: matrixRTCMemberContent()},
	}
}

func matrixRTCMemberContent() map[string]any {
	return voip.BuildRTCMembershipContent(voip.MatrixRTCSession{
		UserID:   "@alice:example.com",
		DeviceID: "DEVICE",
		Intent:   "audio",
		Focus: voip.Focus{
			Type:              "livekit",
			LiveKitServiceURL: "https://rtc.example.com/jwt",
		},
	})
}
