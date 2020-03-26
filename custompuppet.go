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
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/Rhymen/go-whatsapp"
	"maunium.net/go/mautrix"
	appservice "maunium.net/go/mautrix-appservice"
)

var (
	ErrNoCustomMXID    = errors.New("no custom mxid set")
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
	puppet.bridge.AS.StateStore.MarkRegistered(puppet.CustomMXID)
	puppet.Update()
	// TODO leave rooms with default puppet
	return nil
}

func (puppet *Puppet) loginWithSharedSecret(mxid string) (string, error) {
	mac := hmac.New(sha512.New, []byte(puppet.bridge.Config.Bridge.LoginSharedSecret))
	mac.Write([]byte(mxid))
	resp, err := puppet.bridge.AS.BotClient().Login(&mautrix.ReqLogin{
		Type:                     "m.login.password",
		Identifier:               mautrix.UserIdentifier{Type: "m.id.user", User: mxid},
		Password:                 hex.EncodeToString(mac.Sum(nil)),
		DeviceID:                 "WhatsApp Bridge",
		InitialDeviceDisplayName: "WhatsApp Bridge",
	})
	if err != nil {
		return "", err
	}
	return resp.AccessToken, nil
}

func (puppet *Puppet) newCustomIntent() (*appservice.IntentAPI, error) {
	if len(puppet.CustomMXID) == 0 {
		return nil, ErrNoCustomMXID
	}
	client, err := mautrix.NewClient(puppet.bridge.AS.HomeserverURL, puppet.CustomMXID, puppet.AccessToken)
	if err != nil {
		return nil, err
	}
	client.Logger = puppet.bridge.AS.Log.Sub(puppet.CustomMXID)
	client.Syncer = puppet
	client.Store = puppet

	ia := puppet.bridge.AS.NewIntentAPI("custom")
	ia.Client = client
	ia.Localpart = puppet.CustomMXID[1:strings.IndexRune(puppet.CustomMXID, ':')]
	ia.UserID = puppet.CustomMXID
	ia.IsCustomPuppet = true
	return ia, nil
}

func (puppet *Puppet) clearCustomMXID() {
	puppet.CustomMXID = ""
	puppet.AccessToken = ""
	puppet.customIntent = nil
	puppet.customTypingIn = nil
	puppet.customUser = nil
}

func (puppet *Puppet) StartCustomMXID() error {
	if len(puppet.CustomMXID) == 0 {
		puppet.clearCustomMXID()
		return nil
	}
	intent, err := puppet.newCustomIntent()
	if err != nil {
		puppet.clearCustomMXID()
		return err
	}
	urlPath := intent.BuildURL("account", "whoami")
	var resp struct {
		UserID string `json:"user_id"`
	}
	_, err = intent.MakeRequest("GET", urlPath, nil, &resp)
	if err != nil {
		puppet.clearCustomMXID()
		return err
	}
	if resp.UserID != puppet.CustomMXID {
		puppet.clearCustomMXID()
		return ErrMismatchingMXID
	}
	puppet.customIntent = intent
	puppet.customTypingIn = make(map[string]bool)
	puppet.customUser = puppet.bridge.GetUserByMXID(puppet.CustomMXID)
	puppet.startSyncing()
	return nil
}

func (puppet *Puppet) startSyncing() {
	if !puppet.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}
	go func() {
		puppet.log.Debugln("Starting syncing...")
		puppet.customIntent.SyncPresence = "offline"
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

func parseEvent(roomID string, data json.RawMessage) *mautrix.Event {
	event := &mautrix.Event{}
	err := json.Unmarshal(data, event)
	if err != nil {
		// TODO add separate handler for these
		_, _ = fmt.Fprintf(os.Stderr, "Failed to unmarshal event: %v\n%s\n", err, string(data))
		return nil
	}
	return event
}

func parsePresenceEvent(data json.RawMessage) *mautrix.Event {
	event := &mautrix.Event{}
	err := json.Unmarshal(data, event)
	if err != nil {
		// TODO add separate handler for these
		_, _ = fmt.Fprintf(os.Stderr, "Failed to unmarshal event: %v\n%s\n", err, string(data))
		return nil
	}
	return event
}

func (puppet *Puppet) ProcessResponse(resp *mautrix.RespSync, since string) error {
	if !puppet.customUser.IsConnected() {
		puppet.log.Debugln("Skipping sync processing: custom user not connected to whatsapp")
		return nil
	}
	for roomID, events := range resp.Rooms.Join {
		portal := puppet.bridge.GetPortalByMXID(roomID)
		if portal == nil {
			continue
		}
		for _, data := range events.Ephemeral.Events {
			event := parseEvent(roomID, data)
			if event != nil {
				switch event.Type {
				case mautrix.EphemeralEventReceipt:
					go puppet.handleReceiptEvent(portal, event)
				case mautrix.EphemeralEventTyping:
					go puppet.handleTypingEvent(portal, event)
				}
			}
		}
	}
	for _, data := range resp.Presence.Events {
		event := parsePresenceEvent(data)
		if event != nil {
			if event.Sender != puppet.CustomMXID {
				continue
			}
			go puppet.handlePresenceEvent(event)
		}
	}
	return nil
}

func (puppet *Puppet) handlePresenceEvent(event *mautrix.Event) {
	presence := whatsapp.PresenceAvailable
	if event.Content.Raw["presence"].(string) != "online" {
		presence = whatsapp.PresenceUnavailable
		puppet.customUser.log.Infoln("Marking offline")
	} else {
		puppet.customUser.log.Infoln("Marking online")
	}
	_, err := puppet.customUser.Conn.Presence("", presence)
	if err != nil {
		puppet.customUser.log.Warnln("Failed to set presence:", err)
	}
}

func (puppet *Puppet) handleReceiptEvent(portal *Portal, event *mautrix.Event) {
	for eventID, rawReceipts := range event.Content.Raw {
		if receipts, ok := rawReceipts.(map[string]interface{}); !ok {
			continue
		} else if readReceipt, ok := receipts["m.read"].(map[string]interface{}); !ok {
			continue
		} else if _, ok = readReceipt[puppet.CustomMXID].(map[string]interface{}); !ok {
			continue
		}
		message := puppet.bridge.DB.Message.GetByMXID(eventID)
		if message == nil {
			continue
		}
		puppet.customUser.log.Infofln("Marking %s/%s in %s/%s as read", message.JID, message.MXID, portal.Key.JID, portal.MXID)
		_, err := puppet.customUser.Conn.Read(portal.Key.JID, message.JID)
		if err != nil {
			puppet.customUser.log.Warnln("Error marking read:", err)
		}
	}
}

func (puppet *Puppet) handleTypingEvent(portal *Portal, event *mautrix.Event) {
	isTyping := false
	for _, userID := range event.Content.TypingUserIDs {
		if userID == puppet.CustomMXID {
			isTyping = true
			break
		}
	}
	if puppet.customTypingIn[event.RoomID] != isTyping {
		puppet.customTypingIn[event.RoomID] = isTyping
		presence := whatsapp.PresenceComposing
		if !isTyping {
			puppet.customUser.log.Infofln("Marking not typing in %s/%s", portal.Key.JID, portal.MXID)
			presence = whatsapp.PresencePaused
		} else {
			puppet.customUser.log.Infofln("Marking typing in %s/%s", portal.Key.JID, portal.MXID)
		}
		_, err := puppet.customUser.Conn.Presence(portal.Key.JID, presence)
		if err != nil {
			puppet.customUser.log.Warnln("Error setting typing:", err)
		}
	}
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
func (puppet *Puppet) SaveNextBatch(_, nbt string)          { puppet.NextBatch = nbt; puppet.Update() }
func (puppet *Puppet) SaveRoom(room *mautrix.Room)          {}
func (puppet *Puppet) LoadFilterID(_ string) string         { return "" }
func (puppet *Puppet) LoadNextBatch(_ string) string        { return puppet.NextBatch }
func (puppet *Puppet) LoadRoom(roomID string) *mautrix.Room { return nil }
