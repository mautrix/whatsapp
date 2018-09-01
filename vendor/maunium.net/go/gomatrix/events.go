package gomatrix

import (
	"encoding/json"
	"sync"
)

type EventType struct {
	Type    string
	IsState bool
}

func (et *EventType) UnmarshalJSON(data []byte) error {
	err := json.Unmarshal(data, &et.Type)
	if err != nil {
		return err
	}

	switch et.Type {
	case StateAliases.Type, StateCanonicalAlias.Type, StateCreate.Type, StateJoinRules.Type, StateMember.Type,
		StatePowerLevels.Type, StateRoomName.Type, StateRoomAvatar.Type, StateTopic.Type, StatePinnedEvents.Type:
		et.IsState = true
	default:
		et.IsState = false
	}
	return nil
}

func (et *EventType) MarshalJSON() ([]byte, error) {
	return json.Marshal(&et.Type)
}

func (et *EventType) String() string {
	return et.Type
}

type MessageType string

// State events
var (
	StateAliases        = EventType{"m.room.aliases", true}
	StateCanonicalAlias = EventType{"m.room.canonical_alias", true}
	StateCreate         = EventType{"m.room.create", true}
	StateJoinRules      = EventType{"m.room.join_rules", true}
	StateMember         = EventType{"m.room.member", true}
	StatePowerLevels    = EventType{"m.room.power_levels", true}
	StateRoomName       = EventType{"m.room.name", true}
	StateTopic          = EventType{"m.room.topic", true}
	StateRoomAvatar     = EventType{"m.room.avatar", true}
	StatePinnedEvents   = EventType{"m.room.pinned_events", true}
)

// Message events
var (
	EventRedaction = EventType{"m.room.redaction", false}
	EventMessage   = EventType{"m.room.message", false}
	EventSticker   = EventType{"m.sticker", false}
)

// Msgtypes
const (
	MsgText     MessageType = "m.text"
	MsgEmote                = "m.emote"
	MsgNotice               = "m.notice"
	MsgImage                = "m.image"
	MsgLocation             = "m.location"
	MsgVideo                = "m.video"
	MsgAudio                = "m.audio"
	MsgFile                 = "m.file"
)

type Format string

// Message formats
const (
	FormatHTML Format = "org.matrix.custom.html"
)

// Event represents a single Matrix event.
type Event struct {
	StateKey  *string   `json:"state_key,omitempty"` // The state key for the event. Only present on State Events.
	Sender    string    `json:"sender"`              // The user ID of the sender of the event
	Type      EventType `json:"type"`                // The event type
	Timestamp int64     `json:"origin_server_ts"`    // The unix timestamp when this message was sent by the origin server
	ID        string    `json:"event_id"`            // The unique ID of this event
	RoomID    string    `json:"room_id"`             // The room the event was sent to. May be nil (e.g. for presence)
	Content   Content   `json:"content"`             // The JSON content of the event.
	Redacts   string    `json:"redacts,omitempty"`   // The event ID that was redacted if a m.room.redaction event
	Unsigned  Unsigned  `json:"unsigned,omitempty"`  // Unsigned content set by own homeserver.

	InviteRoomState []StrippedState `json:"invite_room_state"`
}

func (evt *Event) GetStateKey() string {
	if evt.StateKey != nil {
		return *evt.StateKey
	}
	return ""
}

type StrippedState struct {
	Content  Content   `json:"content"`
	Type     EventType `json:"type"`
	StateKey string    `json:"state_key"`
}

type Unsigned struct {
	PrevContent   map[string]interface{} `json:"prev_content,omitempty"`
	PrevSender    string                 `json:"prev_sender,omitempty"`
	ReplacesState string                 `json:"replaces_state,omitempty"`
	Age           int64                  `json:"age,omitempty"`
}

type Content struct {
	VeryRaw json.RawMessage        `json:"-"`
	Raw     map[string]interface{} `json:"-"`

	MsgType       MessageType `json:"msgtype,omitempty"`
	Body          string      `json:"body,omitempty"`
	Format        Format      `json:"format,omitempty"`
	FormattedBody string      `json:"formatted_body,omitempty"`

	Info *FileInfo `json:"info,omitempty"`
	URL  string    `json:"url,omitempty"`

	// Membership key for easy access in m.room.member events
	Membership string `json:"membership,omitempty"`

	RelatesTo *RelatesTo `json:"m.relates_to,omitempty"`

	PowerLevels
	Member
	Aliases
	CanonicalAlias
	RoomName
	RoomTopic
}

type serializableContent Content

func (content *Content) UnmarshalJSON(data []byte) error {
	content.VeryRaw = data
	if err := json.Unmarshal(data, &content.Raw); err != nil {
		return err
	}
	return json.Unmarshal(data, (*serializableContent)(content))
}

func (content *Content) UnmarshalPowerLevels() (pl PowerLevels, err error) {
	err = json.Unmarshal(content.VeryRaw, &pl)
	return
}

func (content *Content) UnmarshalMember() (m Member, err error) {
	err = json.Unmarshal(content.VeryRaw, &m)
	return
}

func (content *Content) UnmarshalAliases() (a Aliases, err error) {
	err = json.Unmarshal(content.VeryRaw, &a)
	return
}

func (content *Content) UnmarshalCanonicalAlias() (ca CanonicalAlias, err error) {
	err = json.Unmarshal(content.VeryRaw, &ca)
	return
}

func (content *Content) GetInfo() *FileInfo {
	if content.Info == nil {
		content.Info = &FileInfo{}
	}
	return content.Info
}

type RoomName struct {
	Name string `json:"name,omitempty"`
}

type RoomTopic struct {
	Topic string `json:"topic,omitempty"`
}

type Member struct {
	Membership       string            `json:"membership,omitempty"`
	AvatarURL        string            `json:"avatar_url,omitempty"`
	Displayname      string            `json:"displayname,omitempty"`
	ThirdPartyInvite *ThirdPartyInvite `json:"third_party_invite,omitempty"`
}

type ThirdPartyInvite struct {
	DisplayName string `json:"display_name"`
	Signed      struct {
		Token      string          `json:"token"`
		Signatures json.RawMessage `json:"signatures"`
		MXID       string          `json:"mxid"`
	}
}

type Aliases struct {
	Aliases []string `json:"aliases,omitempty"`
}

type CanonicalAlias struct {
	Alias string `json:"alias,omitempty"`
}

type PowerLevels struct {
	usersLock    sync.RWMutex   `json:"-"`
	Users        map[string]int `json:"users,omitempty"`
	UsersDefault int            `json:"users_default,omitempty"`

	eventsLock    sync.RWMutex      `json:"-"`
	Events        map[string]int `json:"events,omitempty"`
	EventsDefault int               `json:"events_default,omitempty"`

	StateDefaultPtr *int `json:"state_default,omitempty"`

	InvitePtr *int `json:"invite,omitempty"`
	KickPtr   *int `json:"kick,omitempty"`
	BanPtr    *int `json:"ban,omitempty"`
	RedactPtr *int `json:"redact,omitempty"`
}

func (pl *PowerLevels) Invite() int {
	if pl.InvitePtr != nil {
		return *pl.InvitePtr
	}
	return 50
}

func (pl *PowerLevels) Kick() int {
	if pl.KickPtr != nil {
		return *pl.KickPtr
	}
	return 50
}

func (pl *PowerLevels) Ban() int {
	if pl.BanPtr != nil {
		return *pl.BanPtr
	}
	return 50
}

func (pl *PowerLevels) Redact() int {
	if pl.RedactPtr != nil {
		return *pl.RedactPtr
	}
	return 50
}

func (pl *PowerLevels) StateDefault() int {
	if pl.StateDefaultPtr != nil {
		return *pl.StateDefaultPtr
	}
	return 50
}

func (pl *PowerLevels) GetUserLevel(userID string) int {
	pl.usersLock.RLock()
	defer pl.usersLock.RUnlock()
	level, ok := pl.Users[userID]
	if !ok {
		return pl.UsersDefault
	}
	return level
}

func (pl *PowerLevels) SetUserLevel(userID string, level int) {
	pl.usersLock.Lock()
	defer pl.usersLock.Unlock()
	if level == pl.UsersDefault {
		delete(pl.Users, userID)
	} else {
		pl.Users[userID] = level
	}
}

func (pl *PowerLevels) EnsureUserLevel(userID string, level int) bool {
	existingLevel := pl.GetUserLevel(userID)
	if existingLevel != level {
		pl.SetUserLevel(userID, level)
		return true
	}
	return false
}

func (pl *PowerLevels) GetEventLevel(eventType EventType) int {
	pl.eventsLock.RLock()
	defer pl.eventsLock.RUnlock()
	level, ok := pl.Events[eventType.String()]
	if !ok {
		if eventType.IsState {
			return pl.StateDefault()
		}
		return pl.EventsDefault
	}
	return level
}

func (pl *PowerLevels) SetEventLevel(eventType EventType, level int) {
	pl.eventsLock.Lock()
	defer pl.eventsLock.Unlock()
	if (eventType.IsState && level == pl.StateDefault()) || (!eventType.IsState && level == pl.EventsDefault) {
		delete(pl.Events, eventType.String())
	} else {
		pl.Events[eventType.String()] = level
	}
}

func (pl *PowerLevels) EnsureEventLevel(eventType EventType, level int) bool {
	existingLevel := pl.GetEventLevel(eventType)
	if existingLevel != level {
		pl.SetEventLevel(eventType, level)
		return true
	}
	return false
}

type FileInfo struct {
	MimeType      string    `json:"mimetype,omitempty"`
	ThumbnailInfo *FileInfo `json:"thumbnail_info,omitempty"`
	ThumbnailURL  string    `json:"thumbnail_url,omitempty"`
	Height        int       `json:"h,omitempty"`
	Width         int       `json:"w,omitempty"`
	Duration      uint      `json:"duration,omitempty"`
	Size          int       `json:"size,omitempty"`
}

func (fileInfo *FileInfo) GetThumbnailInfo() *FileInfo {
	if fileInfo.ThumbnailInfo == nil {
		fileInfo.ThumbnailInfo = &FileInfo{}
	}
	return fileInfo.ThumbnailInfo
}

type RelatesTo struct {
	InReplyTo InReplyTo `json:"m.in_reply_to,omitempty"`
}

type InReplyTo struct {
	EventID string `json:"event_id,omitempty"`
	// Not required, just for future-proofing
	RoomID string `json:"room_id,omitempty"`
}
