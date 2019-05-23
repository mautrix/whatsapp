// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2019 Tulir Asokan
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
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix-appservice"
)

var (
	ErrNoCustomMXID = errors.New("no custom mxid set")
	ErrMismatchingMXID = errors.New("whoami result does not match custom mxid")
)

func (puppet *Puppet) SwitchCustomMXID(accessToken string, mxid string) error {
	prevCustomMXID := puppet.CustomMXID
	if puppet.customIntent != nil {
		puppet.stopSyncing()
	}
	puppet.CustomMXID = mxid
	puppet.AccessToken = accessToken

	err := puppet.StartCustomMXID()
	if err != nil {
		return err
	}

	if len(prevCustomMXID) > 0 {
		delete(puppet.bridge.puppetsByCustomMXID, prevCustomMXID)
	}
	if len(puppet.CustomMXID) > 0 {
		puppet.bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	puppet.Update()
	// TODO leave rooms with default puppet
	return nil
}

func (puppet *Puppet) newCustomIntent() (*appservice.IntentAPI, error) {
	if len(puppet.CustomMXID) == 0 {
		return nil, ErrNoCustomMXID
	}
	client, err := mautrix.NewClient(puppet.bridge.AS.HomeserverURL, puppet.CustomMXID, puppet.AccessToken)
	if err != nil {
		return nil, err
	}
	client.Store = puppet

	ia := puppet.bridge.AS.NewIntentAPI("custom")
	ia.Client = client
	ia.Localpart = puppet.CustomMXID[1:strings.IndexRune(puppet.CustomMXID, ':')]
	ia.UserID = puppet.CustomMXID
	ia.IsCustomPuppet = true
	return ia, nil
}

func (puppet *Puppet) StartCustomMXID() error {
	if len(puppet.CustomMXID) == 0 {
		return nil
	}
	intent, err := puppet.newCustomIntent()
	if err != nil {
		puppet.CustomMXID = ""
		puppet.AccessToken = ""
		return err
	}
	urlPath := intent.BuildURL("account", "whoami")
	var resp struct{ UserID string `json:"user_id"` }
	_, err = intent.MakeRequest("GET", urlPath, nil, &resp)
	if err != nil {
		puppet.CustomMXID = ""
		puppet.AccessToken = ""
		return err
	}
	if resp.UserID != puppet.CustomMXID {
		puppet.CustomMXID = ""
		puppet.AccessToken = ""
		return ErrMismatchingMXID
	}
	puppet.customIntent = intent
	puppet.startSyncing()
	return nil
}

func (puppet *Puppet) startSyncing() {
	if !puppet.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}
	go func() {
		puppet.log.Debugln("Starting syncing...")
		err := puppet.customIntent.Sync()
		if err != nil {
			puppet.log.Errorln("Fatal error syncing:", err)
		}
	}()
}

func (puppet *Puppet) stopSyncing() {
	if !puppet.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}
	puppet.customIntent.StopSync()
}

func (puppet *Puppet) ProcessResponse(resp *mautrix.RespSync, since string) error {
	puppet.log.Debugln("Sync data:", resp, since)
	// TODO handle sync data
	return nil
}

func (puppet *Puppet) OnFailedSync(res *mautrix.RespSync, err error) (time.Duration, error) {
	puppet.log.Warnln("Sync error:", err)
	return 10 * time.Second, nil
}

func (puppet *Puppet) GetFilterJSON(_ string) json.RawMessage {
	mxid, _ := json.Marshal(puppet.CustomMXID)
	return json.RawMessage(fmt.Sprintf(`{
    "account_data": { "types": [] },
    "presence": {
        "senders": [
            %s
	    ],
        "types": [
            "m.presence"
        ]
    },
    "room": {
        "ephemeral": {
            "types": [
                "m.typing",
                "m.receipt"
            ]
        },
        "include_leave": false,
        "account_data": { "types": [] },
        "state": { "types": [] },
        "timeline": { "types": [] }
    }
}`, mxid))
}

func (puppet *Puppet) SaveFilterID(_, _ string)             {}
func (puppet *Puppet) SaveNextBatch(_, nbt string)          { puppet.NextBatch = nbt }
func (puppet *Puppet) SaveRoom(room *mautrix.Room)          {}
func (puppet *Puppet) LoadFilterID(_ string) string         { return "" }
func (puppet *Puppet) LoadNextBatch(_ string) string        { return puppet.NextBatch }
func (puppet *Puppet) LoadRoom(roomID string) *mautrix.Room { return nil }
