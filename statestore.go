// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"encoding/json"
	"io/ioutil"
	"os"

	"maunium.net/go/gomatrix"
	"maunium.net/go/mautrix-appservice"
)

type AutosavingStateStore struct {
	appservice.StateStore
	Path string
}

func NewAutosavingStateStore(path string) *AutosavingStateStore {
	return &AutosavingStateStore{
		StateStore: appservice.NewBasicStateStore(),
		Path:       path,
	}
}

func (store *AutosavingStateStore) Save() error {
	store.RLock()
	defer store.RUnlock()
	data, err := json.Marshal(store.StateStore)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(store.Path, data, 0600)
}

func (store *AutosavingStateStore) Load() error {
	store.Lock()
	defer store.Unlock()
	data, err := ioutil.ReadFile(store.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, store.StateStore)
}

func (store *AutosavingStateStore) MarkRegistered(userID string) {
	store.StateStore.MarkRegistered(userID)
	store.Save()
}

func (store *AutosavingStateStore) SetMembership(roomID, userID string, membership gomatrix.Membership) {
	store.StateStore.SetMembership(roomID, userID, membership)
	store.Save()
}

func (store *AutosavingStateStore) SetPowerLevels(roomID string, levels *gomatrix.PowerLevels) {
	store.StateStore.SetPowerLevels(roomID, levels)
	store.Save()
}
