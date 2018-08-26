package appservice

import (
	"maunium.net/go/gomatrix"
	"strings"
	"sync"
)

type StateStore interface {
	IsRegistered(userID string) bool
	MarkRegistered(userID string)

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
	registrationsLock sync.RWMutex                    `json:"-"`
	Registrations     map[string]bool                 `json:"registrations"`
	membershipsLock   sync.RWMutex                    `json:"-"`
	Memberships       map[string]map[string]string    `json:"memberships"`
	powerLevelsLock   sync.RWMutex                    `json:"-"`
	PowerLevels       map[string]*gomatrix.PowerLevels `json:"power_levels"`
}

func NewBasicStateStore() *BasicStateStore {
	return &BasicStateStore{
		Registrations: make(map[string]bool),
		Memberships:   make(map[string]map[string]string),
		PowerLevels:   make(map[string]*gomatrix.PowerLevels),
	}
}

func (store *BasicStateStore) IsRegistered(userID string) bool {
	store.registrationsLock.RLock()
	registered, ok := store.Registrations[userID]
	store.registrationsLock.RUnlock()
	return ok && registered
}

func (store *BasicStateStore) MarkRegistered(userID string) {
	store.registrationsLock.Lock()
	store.Registrations[userID] = true
	store.registrationsLock.Unlock()
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
		store.Memberships[roomID] = map[string]string{
			userID: strings.ToLower(membership),
		}
	} else {
		memberships[userID] = strings.ToLower(membership)
	}
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
