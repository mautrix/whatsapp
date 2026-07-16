package voip

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	EventTypeGroupCall       = "org.matrix.msc3401.call"
	EventTypeGroupCallMember = "org.matrix.msc3401.call.member"
	EventTypeRTCMembership   = "org.matrix.msc4143.rtc.member"
	EventTypeRTCNotification = "org.matrix.msc4075.rtc.notification"
	EventTypeCallNotify      = "org.matrix.msc4075.call.notify"
	EventTypeRTCDecline      = "org.matrix.msc4310.rtc.decline"

	MatrixRTCApplicationCall = "m.call"
	MatrixRTCDefaultSlotID   = "m.call#ROOM"
	MatrixRTCMembershipV1    = "org.matrix.msc4143.rtc.member"
)

var supportedMatrixRTCEventTypes = []event.Type{
	{Type: EventTypeGroupCall, Class: event.StateEventType},
	{Type: EventTypeGroupCallMember, Class: event.StateEventType},
	{Type: EventTypeRTCMembership, Class: event.StateEventType},
	{Type: EventTypeRTCMembership, Class: event.MessageEventType},
	{Type: EventTypeRTCNotification, Class: event.MessageEventType},
	{Type: EventTypeCallNotify, Class: event.MessageEventType},
	{Type: EventTypeRTCDecline, Class: event.MessageEventType},
}

type MatrixRTCEventKind string

const (
	MatrixRTCEventKindUnknown          MatrixRTCEventKind = ""
	MatrixRTCEventKindGroupCall        MatrixRTCEventKind = "group_call"
	MatrixRTCEventKindGroupCallMember  MatrixRTCEventKind = "group_call_member"
	MatrixRTCEventKindRTCMembership    MatrixRTCEventKind = "rtc_membership"
	MatrixRTCEventKindRTCNotification  MatrixRTCEventKind = "rtc_notification"
	MatrixRTCEventKindLegacyCallNotify MatrixRTCEventKind = "legacy_call_notify"
	MatrixRTCEventKindRTCDecline       MatrixRTCEventKind = "rtc_decline"
)

type MatrixRTCEvent struct {
	Type          event.Type
	Kind          MatrixRTCEventKind
	RoomID        id.RoomID
	Sender        id.UserID
	StateKey      string
	CallID        string
	DeviceID      string
	SessionID     string
	Intent        string
	LifetimeMS    int
	FociPreferred []Focus
	Raw           map[string]any
}

type MatrixRTCSession struct {
	UserID              id.UserID
	DeviceID            string
	MemberID            string
	CallID              string
	Intent              string
	Focus               Focus
	Created             time.Time
	Expires             time.Duration
	StickyKey           string
	NotificationEventID id.EventID
}

func SupportedMatrixRTCEventTypes() []event.Type {
	return append([]event.Type(nil), supportedMatrixRTCEventTypes...)
}

func ClassifyMatrixRTCEventType(evtType event.Type) MatrixRTCEventKind {
	switch evtType.Type {
	case EventTypeGroupCall:
		return MatrixRTCEventKindGroupCall
	case EventTypeGroupCallMember:
		return MatrixRTCEventKindGroupCallMember
	case EventTypeRTCMembership:
		return MatrixRTCEventKindRTCMembership
	case EventTypeRTCNotification:
		return MatrixRTCEventKindRTCNotification
	case EventTypeCallNotify:
		return MatrixRTCEventKindLegacyCallNotify
	case EventTypeRTCDecline:
		return MatrixRTCEventKindRTCDecline
	default:
		return MatrixRTCEventKindUnknown
	}
}

func ParseMatrixRTCEvent(evt *event.Event) (MatrixRTCEvent, bool) {
	if evt == nil {
		return MatrixRTCEvent{}, false
	}
	kind := ClassifyMatrixRTCEventType(evt.Type)
	if kind == MatrixRTCEventKindUnknown {
		return MatrixRTCEvent{}, false
	}
	raw := rawMatrixRTCContent(evt)
	parsed := MatrixRTCEvent{
		Type:   evt.Type,
		Kind:   kind,
		RoomID: evt.RoomID,
		Sender: evt.Sender,
		Raw:    raw,
	}
	if evt.StateKey != nil {
		parsed.StateKey = *evt.StateKey
	}
	fillMatrixRTCFields(&parsed, raw)
	return parsed, true
}

func rawMatrixRTCContent(evt *event.Event) map[string]any {
	if evt.Content.Raw != nil {
		return evt.Content.Raw
	}
	if len(evt.Content.VeryRaw) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(evt.Content.VeryRaw, &raw); err == nil && raw != nil {
			return raw
		}
	}
	if evt.Content.Parsed != nil {
		data, err := json.Marshal(evt.Content.Parsed)
		if err == nil {
			var raw map[string]any
			if err = json.Unmarshal(data, &raw); err == nil && raw != nil {
				return raw
			}
		}
	}
	return map[string]any{}
}

func fillMatrixRTCFields(parsed *MatrixRTCEvent, raw map[string]any) {
	parsed.CallID = firstString(raw, "call_id", "m.call_id", "callId", "callID")
	parsed.DeviceID = firstString(raw, "device_id", "m.device_id", "deviceId", "deviceID")
	parsed.SessionID = firstString(raw, "session_id", "m.session_id", "sessionId", "sessionID")
	parsed.Intent = firstString(raw, "intent", "m.call.intent", "call_intent")
	parsed.LifetimeMS = firstInt(raw, "lifetime", "lifetime_ms", "m.lifetime", "m.lifetime_ms")
	forEachObject(raw["application"], func(application map[string]any) {
		if parsed.Intent == "" {
			parsed.Intent = firstString(application, "intent", "m.call.intent", "call_intent")
		}
	})
	parsed.FociPreferred = append(parsed.FociPreferred, parseFoci(raw["rtc_transports"])...)
	parsed.FociPreferred = append(parsed.FociPreferred, parseFoci(raw["foci_preferred"])...)
	parsed.FociPreferred = append(parsed.FociPreferred, parseFoci(raw["m.foci_preferred"])...)

	forEachObject(raw["memberships"], func(membership map[string]any) {
		if parsed.CallID == "" {
			parsed.CallID = firstString(membership, "call_id", "m.call_id", "callId", "callID")
		}
		if parsed.DeviceID == "" {
			parsed.DeviceID = firstString(membership, "device_id", "m.device_id", "deviceId", "deviceID")
		}
		if parsed.SessionID == "" {
			parsed.SessionID = firstString(membership, "session_id", "m.session_id", "sessionId", "sessionID")
		}
		if parsed.Intent == "" {
			parsed.Intent = firstString(membership, "intent", "m.call.intent", "call_intent")
		}
		if parsed.LifetimeMS == 0 {
			parsed.LifetimeMS = firstInt(membership, "lifetime", "lifetime_ms", "m.lifetime", "m.lifetime_ms")
		}
		forEachObject(membership["application"], func(application map[string]any) {
			if parsed.Intent == "" {
				parsed.Intent = firstString(application, "intent", "m.call.intent", "call_intent")
			}
		})
		parsed.FociPreferred = append(parsed.FociPreferred, parseFoci(membership["rtc_transports"])...)
		parsed.FociPreferred = append(parsed.FociPreferred, parseFoci(membership["foci_preferred"])...)
		parsed.FociPreferred = append(parsed.FociPreferred, parseFoci(membership["m.foci_preferred"])...)
	})

	if parsed.DeviceID == "" {
		parsed.DeviceID = parsed.StateKey
	}
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if str, ok := value.(string); ok {
				return str
			}
		}
	}
	return ""
}

func firstInt(raw map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed
		case int64:
			return int(typed)
		case float64:
			return int(typed)
		case json.Number:
			if integer, err := typed.Int64(); err == nil {
				return int(integer)
			}
		}
	}
	return 0
}

func MatrixRTCEventHasJoinContent(evt MatrixRTCEvent) bool {
	switch evt.Kind {
	case MatrixRTCEventKindRTCMembership, MatrixRTCEventKindGroupCallMember:
		return matrixRTCContentHasJoinData(evt.Raw)
	default:
		return false
	}
}

func matrixRTCContentHasJoinData(raw map[string]any) bool {
	if len(raw) == 0 {
		return false
	}
	if matrixRTCModernContentHasJoinData(raw) || matrixRTCLegacyContentHasJoinData(raw) {
		return true
	}
	hasJoin := false
	forEachObject(raw["memberships"], func(membership map[string]any) {
		if matrixRTCMembershipArrayItemHasJoinData(membership) {
			hasJoin = true
		}
	})
	return hasJoin
}

func matrixRTCModernContentHasJoinData(raw map[string]any) bool {
	if slotID := firstString(raw, "slot_id"); slotID != "" && slotID != MatrixRTCDefaultSlotID {
		return false
	}
	return matrixRTCApplicationIsCall(raw["application"]) &&
		matrixRTCContentHasMember(raw) &&
		len(parseFoci(raw["rtc_transports"])) > 0
}

func matrixRTCLegacyContentHasJoinData(raw map[string]any) bool {
	if !matrixRTCApplicationIsCall(raw["application"]) ||
		!matrixRTCContentHasIdentifier(raw) ||
		!matrixRTCContentHasPositiveLifetime(raw) {
		return false
	}
	return len(parseFoci(raw["foci_preferred"])) > 0 ||
		len(parseFoci(raw["m.foci_preferred"])) > 0
}

func matrixRTCMembershipArrayItemHasJoinData(raw map[string]any) bool {
	if application, ok := raw["application"]; ok && !matrixRTCApplicationIsCall(application) {
		return false
	}
	if !matrixRTCContentHasIdentifier(raw) || !matrixRTCContentHasPositiveLifetime(raw) {
		return false
	}
	return len(parseFoci(raw["rtc_transports"])) > 0 ||
		len(parseFoci(raw["foci_preferred"])) > 0 ||
		len(parseFoci(raw["m.foci_preferred"])) > 0
}

func matrixRTCApplicationIsCall(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed == MatrixRTCApplicationCall
	case map[string]any:
		return firstString(typed, "type", "application") == MatrixRTCApplicationCall
	case []any:
		for _, item := range typed {
			if matrixRTCApplicationIsCall(item) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range typed {
			if matrixRTCApplicationIsCall(item) {
				return true
			}
		}
	}
	return false
}

func matrixRTCContentHasMember(raw map[string]any) bool {
	hasMember := false
	forEachObject(raw["member"], func(member map[string]any) {
		if firstString(member, "user_id", "device_id", "id") != "" {
			hasMember = true
		}
	})
	return hasMember
}

func matrixRTCContentHasIdentifier(raw map[string]any) bool {
	return matrixRTCContentHasMember(raw) ||
		firstString(raw, "membershipID", "membership_id", "device_id", "m.device_id", "deviceId", "deviceID", "session_id", "m.session_id", "sessionId", "sessionID") != ""
}

func matrixRTCContentHasPositiveLifetime(raw map[string]any) bool {
	for _, key := range []string{"expires", "lifetime", "lifetime_ms", "m.lifetime", "m.lifetime_ms"} {
		if _, ok := raw[key]; ok {
			return firstInt(raw, key) > 0
		}
	}
	return true
}

func forEachObject(value any, fn func(map[string]any)) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				fn(object)
			}
		}
	case []map[string]any:
		for _, item := range typed {
			fn(item)
		}
	case map[string]any:
		fn(typed)
	}
}

func parseFoci(value any) []Focus {
	var output []Focus
	forEachObject(value, func(rawFocus map[string]any) {
		if firstString(rawFocus, "type") != "livekit" {
			return
		}
		serviceURL := firstString(rawFocus, "livekit_service_url", "livekit_service_url_prefix", "service_url")
		if serviceURL == "" {
			return
		}
		output = append(output, Focus{
			Type:              "livekit",
			LiveKitServiceURL: serviceURL,
		})
	})
	return output
}

func MatrixRTCDeviceID(loginID, waCallID string) string {
	sum := sha256.Sum256([]byte(loginID + "\x00" + waCallID))
	return "WA" + hex.EncodeToString(sum[:8])
}

func MatrixRTCMemberID(userID id.UserID, deviceID string) string {
	if deviceID == "" {
		return userID.String()
	}
	return userID.String() + ":" + deviceID
}

func MatrixRTCStateKey(userID id.UserID, deviceID string) string {
	if deviceID == "" {
		return userID.String()
	}
	return userID.String() + "_" + deviceID
}

func RTCMembershipEventType(class event.TypeClass) event.Type {
	return event.Type{Type: EventTypeRTCMembership, Class: class}
}

func GroupCallMemberEventType() event.Type {
	return event.Type{Type: EventTypeGroupCallMember, Class: event.StateEventType}
}

func RTCNotificationEventType() event.Type {
	return event.Type{Type: EventTypeRTCNotification, Class: event.MessageEventType}
}

func LegacyCallNotifyEventType() event.Type {
	return event.Type{Type: EventTypeCallNotify, Class: event.MessageEventType}
}

func BuildRTCMembershipContent(session MatrixRTCSession) map[string]any {
	deviceID := session.DeviceID
	memberID := session.MemberID
	if memberID == "" {
		memberID = MatrixRTCMemberID(session.UserID, deviceID)
	}
	stickyKey := session.StickyKey
	if stickyKey == "" {
		stickyKey = memberID
	}
	intent := session.Intent
	if intent == "" {
		intent = "audio"
	}
	application := map[string]any{
		"type":          MatrixRTCApplicationCall,
		"m.call.intent": intent,
	}
	content := map[string]any{
		"slot_id": MatrixRTCDefaultSlotID,
		"member": map[string]any{
			"user_id":   session.UserID.String(),
			"device_id": deviceID,
			"id":        memberID,
		},
		"application":        application,
		"rtc_transports":     []map[string]any{liveKitTransport(session.Focus)},
		"versions":           []string{MatrixRTCMembershipV1},
		"sticky_key":         stickyKey,
		"msc4354_sticky_key": stickyKey,
	}
	if session.NotificationEventID != "" {
		content["m.relates_to"] = map[string]any{
			"rel_type": "m.reference",
			"event_id": session.NotificationEventID.String(),
		}
	}
	return content
}

func BuildLegacyCallMemberContent(session MatrixRTCSession) map[string]any {
	deviceID := session.DeviceID
	memberID := session.MemberID
	if memberID == "" {
		memberID = MatrixRTCMemberID(session.UserID, deviceID)
	}
	intent := session.Intent
	if intent == "" {
		intent = "audio"
	}
	created := session.Created
	if created.IsZero() {
		created = time.Now()
	}
	expires := session.Expires
	if expires <= 0 {
		expires = 4 * time.Hour
	}
	return map[string]any{
		"application": MatrixRTCApplicationCall,
		"call_id":     "",
		"device_id":   deviceID,
		"focus_active": map[string]any{
			"type":            "livekit",
			"focus_selection": "multi_sfu",
		},
		"foci_preferred": []map[string]any{liveKitTransport(session.Focus)},
		"created_ts":     created.UnixMilli(),
		"scope":          "m.room",
		"expires":        expires.Milliseconds(),
		"m.call.intent":  intent,
		"membershipID":   memberID,
	}
}

func BuildRTCNotificationContent(now time.Time, lifetime time.Duration, intent string) map[string]any {
	if now.IsZero() {
		now = time.Now()
	}
	if lifetime <= 0 || lifetime > 90*time.Second {
		lifetime = 90 * time.Second
	}
	if intent == "" {
		intent = "audio"
	}
	return map[string]any{
		"notification_type": "ring",
		"sender_ts":         now.UnixMilli(),
		"lifetime":          lifetime.Milliseconds(),
		"m.call.intent":     intent,
		"m.mentions":        map[string]any{},
	}
}

func BuildLegacyCallNotifyContent(callID, intent string) map[string]any {
	if intent == "" {
		intent = "audio"
	}
	return map[string]any{
		"application":   MatrixRTCApplicationCall,
		"notify_type":   "ring",
		"call_id":       callID,
		"m.call.intent": intent,
		"m.mentions":    map[string]any{},
	}
}

func EmptyMatrixRTCContent(stickyKey string) map[string]any {
	if stickyKey == "" {
		return map[string]any{}
	}
	return map[string]any{
		"sticky_key":         stickyKey,
		"msc4354_sticky_key": stickyKey,
	}
}

func liveKitTransport(focus Focus) map[string]any {
	transport := map[string]any{
		"type": "livekit",
	}
	if focus.LiveKitServiceURL != "" {
		transport["livekit_service_url"] = focus.LiveKitServiceURL
	}
	return transport
}
