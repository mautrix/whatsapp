package appservice

import (
	"maunium.net/go/gomatrix"
	"strings"
	"sync"
	"time"
)

type StateStore interface {
	IsRegistered(userID string) bool
	MarkRegistered(userID string)

	IsTyping(roomID, userID string) bool
	SetTyping(roomID, userID string, timeout int64)

	IsInRoom(roomID, userID string) bool
	SetMembership(roomID, userID, membership string)

	SetPowerLevels(roomID string, levels *gomatrix.PowerLevels)
	GetPowerLevels(roomID string) *gomatrix.PowerLevels
	GetPowerLevel(roomID, userID string) int
	GetPowerLevelRequirement(roomID string, eventType gomatrix.EventType, isState bool) int
	HasPowerLevel(roomID, userID string, eventType gomatrix.EventType, isState bool) bool
}

func (as *AppService) UpdateState(evt *gomatrix.Event) {
	switch evt.Type {
	case gomatrix.StateMember:
		as.StateStore.SetMembership(evt.RoomID, evt.GetStateKey(), evt.Content.Membership)
	}
}

type BasicStateStore struct {
	registrationsLock sync.RWMutex                     `json:"-"`
	Registrations     map[string]bool                  `json:"registrations"`
	membershipsLock   sync.RWMutex                     `json:"-"`
	Memberships       map[string]map[string]string     `json:"memberships"`
	powerLevelsLock   sync.RWMutex                     `json:"-"`
	PowerLevels       map[string]*gomatrix.PowerLevels `json:"power_levels"`

	Typing     map[string]map[string]int64 `json:"-"`
	typingLock sync.RWMutex                `json:"-"`
}

func NewBasicStateStore() StateStore {
	return &BasicStateStore{
		Registrations: make(map[string]bool),
		Memberships:   make(map[string]map[string]string),
		PowerLevels:   make(map[string]*gomatrix.PowerLevels),
		Typing:        make(map[string]map[string]int64),
	}
}

func (store *BasicStateStore) IsRegistered(userID string) bool {
	store.registrationsLock.RLock()
	defer store.registrationsLock.RUnlock()
	registered, ok := store.Registrations[userID]
	return ok && registered
}

func (store *BasicStateStore) MarkRegistered(userID string) {
	store.registrationsLock.Lock()
	defer store.registrationsLock.Unlock()
	store.Registrations[userID] = true
}

func (store *BasicStateStore) IsTyping(roomID, userID string) bool {
	store.typingLock.RLock()
	defer store.typingLock.RUnlock()
	roomTyping, ok := store.Typing[roomID]
	if !ok {
		return false
	}
	typingEndsAt, _ := roomTyping[userID]
	return typingEndsAt >= time.Now().Unix()
}

func (store *BasicStateStore) SetTyping(roomID, userID string, timeout int64) {
	store.typingLock.Lock()
	defer store.typingLock.Unlock()
	roomTyping, ok := store.Typing[roomID]
	if !ok {
		if timeout >= 0 {
			roomTyping = map[string]int64{
				userID: time.Now().Unix() + timeout,
			}
		} else {
			roomTyping = make(map[string]int64)
		}
	} else {
		if timeout >= 0 {
			roomTyping[userID] = time.Now().Unix() + timeout
		} else {
			delete(roomTyping, userID)
		}
	}
	store.Typing[roomID] = roomTyping
}

func (store *BasicStateStore) GetRoomMemberships(roomID string) map[string]string {
	store.membershipsLock.RLock()
	memberships, ok := store.Memberships[roomID]
	store.membershipsLock.RUnlock()
	if !ok {
		memberships = make(map[string]string)
		store.membershipsLock.Lock()
		store.Memberships[roomID] = memberships
		store.membershipsLock.Unlock()
	}
	return memberships
}

func (store *BasicStateStore) GetMembership(roomID, userID string) string {
	store.membershipsLock.RLock()
	membership, ok := store.GetRoomMemberships(roomID)[userID]
	store.membershipsLock.RUnlock()
	if !ok {
		return "leave"
	}
	return membership
}

func (store *BasicStateStore) IsInRoom(roomID, userID string) bool {
	return store.GetMembership(roomID, userID) == "join"
}

func (store *BasicStateStore) SetMembership(roomID, userID, membership string) {
	store.membershipsLock.Lock()
	memberships, ok := store.Memberships[roomID]
	if !ok {
		memberships = map[string]string{
			userID: strings.ToLower(membership),
		}
	} else {
		memberships[userID] = strings.ToLower(membership)
	}
	store.Memberships[roomID] = memberships
	store.membershipsLock.Unlock()
}

func (store *BasicStateStore) SetPowerLevels(roomID string, levels *gomatrix.PowerLevels) {
	store.powerLevelsLock.Lock()
	store.PowerLevels[roomID] = levels
	store.powerLevelsLock.Unlock()
}

func (store *BasicStateStore) GetPowerLevels(roomID string) (levels *gomatrix.PowerLevels) {
	store.powerLevelsLock.RLock()
	levels, _ = store.PowerLevels[roomID]
	store.powerLevelsLock.RUnlock()
	return
}

func (store *BasicStateStore) GetPowerLevel(roomID, userID string) int {
	return store.GetPowerLevels(roomID).GetUserLevel(userID)
}

func (store *BasicStateStore) GetPowerLevelRequirement(roomID string, eventType gomatrix.EventType, isState bool) int {
	levels := store.GetPowerLevels(roomID)
	switch eventType {
	case "kick":
		return levels.Kick()
	case "invite":
		return levels.Invite()
	case "redact":
		return levels.Redact()
	}
	return levels.GetEventLevel(eventType, isState)
}

func (store *BasicStateStore) HasPowerLevel(roomID, userID string, eventType gomatrix.EventType, isState bool) bool {
	return store.GetPowerLevel(roomID, userID) >= store.GetPowerLevelRequirement(roomID, eventType, isState)
}
