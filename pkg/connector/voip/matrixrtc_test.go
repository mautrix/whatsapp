package voip

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestSupportedMatrixRTCEventTypesHaveExplicitClasses(t *testing.T) {
	types := SupportedMatrixRTCEventTypes()
	if len(types) != 10 {
		t.Fatalf("SupportedMatrixRTCEventTypes returned %d types, want 10", len(types))
	}
	for _, evtType := range types {
		switch evtType.Type {
		case EventTypeGroupCall, EventTypeGroupCallMember:
			if evtType.Class != event.StateEventType {
				t.Fatalf("%s class = %v, want state", evtType.Type, evtType.Class)
			}
		case EventTypeRTCMembership:
			if evtType.Class != event.StateEventType && evtType.Class != event.MessageEventType {
				t.Fatalf("%s class = %v, want state or message", evtType.Type, evtType.Class)
			}
		case EventTypeRTCNotification, EventTypeCallNotify, EventTypeRTCDecline,
			EventTypeElementCallReaction, event.EventReaction.Type, event.EventRedaction.Type:
			if evtType.Class != event.MessageEventType {
				t.Fatalf("%s class = %v, want message", evtType.Type, evtType.Class)
			}
		default:
			t.Fatalf("unexpected MatrixRTC event type %s", evtType.Type)
		}
	}
}

func TestParseElementCallReactionEvent(t *testing.T) {
	evt := &event.Event{
		ID:     id.EventID("$reaction"),
		Type:   ElementCallReactionEventType(),
		RoomID: id.RoomID("!room:example.com"),
		Sender: id.UserID("@alice:example.com"),
		Content: event.Content{Raw: map[string]any{
			"m.relates_to": map[string]any{
				"rel_type": "m.reference",
				"event_id": "$membership",
			},
			"emoji": "❤️",
			"name":  "generic",
		}},
	}
	parsed, ok := ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatal("ParseMatrixRTCEvent did not recognize Element Call reaction")
	}
	if parsed.Kind != MatrixRTCEventKindCallReaction ||
		parsed.EventID != "$reaction" ||
		parsed.RelatesToEventID != "$membership" ||
		parsed.ReactionEmoji != "❤️" {
		t.Fatalf("unexpected parsed reaction: %+v", parsed)
	}
}

func TestSupportedWhatsAppCallReactions(t *testing.T) {
	for _, emoji := range []string{"👍", "❤️", "😂", "😮", "😢", "🙏"} {
		normalized, ok := NormalizeWhatsAppCallReaction(emoji)
		if !ok || normalized != emoji {
			t.Fatalf("NormalizeWhatsAppCallReaction(%q) = %q, %t", emoji, normalized, ok)
		}
	}
	if normalized, ok := NormalizeWhatsAppCallReaction("🎉"); ok || normalized != "" {
		t.Fatalf("unsupported reaction normalized to %q, %t", normalized, ok)
	}
}

func TestParseMatrixRTCDeclineEvent(t *testing.T) {
	evt := &event.Event{
		Type:   event.Type{Type: EventTypeRTCDecline, Class: event.MessageEventType},
		RoomID: id.RoomID("!room:example.com"),
		Sender: id.UserID("@alice:example.com"),
		Content: event.Content{Raw: map[string]any{
			"call_id":    "call-1",
			"device_id":  "DEVICE",
			"session_id": "SESSION",
		}},
	}
	parsed, ok := ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize decline event")
	}
	if parsed.Kind != MatrixRTCEventKindRTCDecline {
		t.Fatalf("kind = %q, want %q", parsed.Kind, MatrixRTCEventKindRTCDecline)
	}
	if parsed.CallID != "call-1" || parsed.DeviceID != "DEVICE" || parsed.SessionID != "SESSION" {
		t.Fatalf("unexpected parsed event: %+v", parsed)
	}
}

func TestParseMatrixRTCMembershipEvent(t *testing.T) {
	stateKey := "@alice:example.com"
	evt := &event.Event{
		Type:     event.Type{Type: EventTypeRTCMembership, Class: event.StateEventType},
		RoomID:   id.RoomID("!room:example.com"),
		Sender:   id.UserID("@alice:example.com"),
		StateKey: &stateKey,
		Content: event.Content{Raw: map[string]any{
			"memberships": []any{map[string]any{
				"call_id":     "call-2",
				"device_id":   "DEVICE",
				"session_id":  "SESSION",
				"lifetime_ms": float64(60000),
				"foci_preferred": []any{map[string]any{
					"type":                "livekit",
					"livekit_service_url": "https://rtc.example.com/jwt",
				}},
			}},
		}},
	}
	parsed, ok := ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership event")
	}
	if parsed.Kind != MatrixRTCEventKindRTCMembership || parsed.StateKey != stateKey {
		t.Fatalf("unexpected parsed event metadata: %+v", parsed)
	}
	if parsed.CallID != "call-2" || parsed.DeviceID != "DEVICE" || parsed.SessionID != "SESSION" {
		t.Fatalf("unexpected parsed event identifiers: %+v", parsed)
	}
	if parsed.LifetimeMS != 60000 {
		t.Fatalf("LifetimeMS = %d, want 60000", parsed.LifetimeMS)
	}
	if len(parsed.FociPreferred) != 1 || parsed.FociPreferred[0].LiveKitServiceURL != "https://rtc.example.com/jwt" {
		t.Fatalf("unexpected foci: %+v", parsed.FociPreferred)
	}
}

func TestBuildRTCMembershipContent(t *testing.T) {
	session := MatrixRTCSession{
		UserID:   "@wa_123:example.com",
		DeviceID: "WADEVICE",
		Focus: Focus{
			Type:              "livekit",
			LiveKitServiceURL: "https://rtc.example.com/jwt",
		},
	}
	content := BuildRTCMembershipContent(session)
	if content["slot_id"] != MatrixRTCDefaultSlotID {
		t.Fatalf("slot_id = %q, want %q", content["slot_id"], MatrixRTCDefaultSlotID)
	}
	member := content["member"].(map[string]any)
	if member["user_id"] != "@wa_123:example.com" || member["device_id"] != "WADEVICE" {
		t.Fatalf("unexpected member: %+v", member)
	}
	transports := content["rtc_transports"].([]map[string]any)
	if len(transports) != 1 || transports[0]["livekit_service_url"] != "https://rtc.example.com/jwt" {
		t.Fatalf("unexpected transports: %+v", transports)
	}
}

func TestParseBuiltRTCMembershipContent(t *testing.T) {
	content := BuildRTCMembershipContent(MatrixRTCSession{
		UserID:   "@wa_123:example.com",
		DeviceID: "WADEVICE",
		Intent:   "audio",
		Focus: Focus{
			Type:              "livekit",
			LiveKitServiceURL: "https://rtc.example.com/jwt",
		},
	})
	evt := &event.Event{
		Type:    RTCMembershipEventType(event.MessageEventType),
		RoomID:  id.RoomID("!room:example.com"),
		Sender:  id.UserID("@wa_123:example.com"),
		Content: event.Content{Raw: content},
	}
	parsed, ok := ParseMatrixRTCEvent(evt)
	if !ok {
		t.Fatalf("ParseMatrixRTCEvent did not recognize membership event")
	}
	if parsed.Intent != "audio" {
		t.Fatalf("Intent = %q, want audio", parsed.Intent)
	}
	if len(parsed.FociPreferred) != 1 || parsed.FociPreferred[0].LiveKitServiceURL != "https://rtc.example.com/jwt" {
		t.Fatalf("unexpected foci: %+v", parsed.FociPreferred)
	}
	if !MatrixRTCEventHasJoinContent(parsed) {
		t.Fatalf("MatrixRTCEventHasJoinContent returned false for a built membership")
	}
}

func TestMatrixRTCEventHasJoinContentRejectsStickyCleanup(t *testing.T) {
	evt := MatrixRTCEvent{
		Kind: MatrixRTCEventKindRTCMembership,
		Raw:  EmptyMatrixRTCContent("sticky"),
	}
	if MatrixRTCEventHasJoinContent(evt) {
		t.Fatalf("MatrixRTCEventHasJoinContent returned true for sticky cleanup content")
	}
}

func TestMatrixRTCEventHasJoinContentRejectsMetadataWithoutFocus(t *testing.T) {
	for _, raw := range []map[string]any{
		{
			"application": map[string]any{"type": MatrixRTCApplicationCall},
		},
		{
			"slot_id":     MatrixRTCDefaultSlotID,
			"application": map[string]any{"type": MatrixRTCApplicationCall},
			"member":      map[string]any{"user_id": "@alice:example.com", "device_id": "DEVICE"},
		},
	} {
		evt := MatrixRTCEvent{Kind: MatrixRTCEventKindRTCMembership, Raw: raw}
		if MatrixRTCEventHasJoinContent(evt) {
			t.Fatalf("MatrixRTCEventHasJoinContent returned true for metadata-only content: %+v", raw)
		}
	}
}

func TestBuildRTCNotificationContentCapsLifetime(t *testing.T) {
	content := BuildRTCNotificationContent(testTime, 5*time.Minute, "audio")
	if content["notification_type"] != "ring" {
		t.Fatalf("notification_type = %q, want ring", content["notification_type"])
	}
	if content["lifetime"] != int64(90000) {
		t.Fatalf("lifetime = %v, want 90000", content["lifetime"])
	}
}

var testTime = time.Unix(123, 0)
