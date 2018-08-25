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
	"maunium.net/go/gomatrix"
	"maunium.net/go/mautrix-appservice"
	"encoding/json"
	"io/ioutil"
	"os"
)

type AutosavingStateStore struct {
	*appservice.BasicStateStore
	Path string
}

func NewAutosavingStateStore(path string) *AutosavingStateStore {
	return &AutosavingStateStore{
		BasicStateStore: appservice.NewBasicStateStore(),
		Path:            path,
	}
}

func (store *AutosavingStateStore) Save() error {
	data, err := json.Marshal(store.BasicStateStore)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(store.Path, data, 0600)
}

func (store *AutosavingStateStore) Load() error {
	data, err := ioutil.ReadFile(store.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, store.BasicStateStore)
}

func (store *AutosavingStateStore) MarkRegistered(userID string) {
	store.BasicStateStore.MarkRegistered(userID)
	store.Save()
}

func (store *AutosavingStateStore) SetMembership(roomID, userID, membership string) {
	store.BasicStateStore.SetMembership(roomID, userID, membership)
	store.Save()
}

func (store *AutosavingStateStore) SetPowerLevels(roomID string, levels gomatrix.PowerLevels) {
	store.BasicStateStore.SetPowerLevels(roomID, levels)
	store.Save()
}