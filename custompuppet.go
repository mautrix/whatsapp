// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"errors"
	"time"

	"github.com/Rhymen/go-whatsapp"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	ErrNoCustomMXID    = errors.New("no custom mxid set")
	ErrMismatchingMXID = errors.New("whoami result does not match custom mxid")
)

func (puppet *Puppet) SwitchCustomMXID(accessToken string, mxid id.UserID) error {
	prevCustomMXID := puppet.CustomMXID
	if puppet.customIntent != nil {
		puppet.stopSyncing()
	}
	puppet.CustomMXID = mxid
	puppet.AccessToken = accessToken

	err := puppet.StartCustomMXID(false)
	if err != nil {
		return err
	}

	if len(prevCustomMXID) > 0 {
		delete(puppet.bridge.puppetsByCustomMXID, prevCustomMXID)
	}
	if len(puppet.CustomMXID) > 0 {
		puppet.bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	puppet.EnablePresence = puppet.bridge.Config.Bridge.DefaultBridgePresence
	puppet.EnableReceipts = puppet.bridge.Config.Bridge.DefaultBridgeReceipts
	puppet.bridge.AS.StateStore.MarkRegistered(puppet.CustomMXID)
	puppet.Update()
	// TODO leave rooms with default puppet
	return nil
}

func (puppet *Puppet) loginWithSharedSecret(mxid id.UserID) (string, error) {
	puppet.log.Debugfln("Logging into %s with shared secret", mxid)
	mac := hmac.New(sha512.New, []byte(puppet.bridge.Config.Bridge.LoginSharedSecret))
	mac.Write([]byte(mxid))
	resp, err := puppet.bridge.AS.BotClient().Login(&mautrix.ReqLogin{
		Type:                     mautrix.AuthTypePassword,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: string(mxid)},
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
	client.Logger = puppet.bridge.AS.Log.Sub(string(puppet.CustomMXID))
	client.Client = puppet.bridge.AS.HTTPClient
	client.DefaultHTTPRetries = puppet.bridge.AS.DefaultHTTPRetries
	client.Syncer = puppet
	client.Store = puppet

	ia := puppet.bridge.AS.NewIntentAPI("custom")
	ia.Client = client
	ia.Localpart, _, _ = puppet.CustomMXID.Parse()
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

func (puppet *Puppet) StartCustomMXID(reloginOnFail bool) error {
	if len(puppet.CustomMXID) == 0 {
		puppet.clearCustomMXID()
		return nil
	}
	intent, err := puppet.newCustomIntent()
	if err != nil {
		puppet.clearCustomMXID()
		return err
	}
	resp, err := intent.Whoami()
	if err != nil {
		if !reloginOnFail || (errors.Is(err, mautrix.MUnknownToken) && !puppet.tryRelogin(err, "initializing double puppeting")) {
			puppet.clearCustomMXID()
			return err
		}
		intent.AccessToken = puppet.AccessToken
	} else if resp.UserID != puppet.CustomMXID {
		puppet.clearCustomMXID()
		return ErrMismatchingMXID
	}
	puppet.customIntent = intent
	puppet.customTypingIn = make(map[id.RoomID]bool)
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

func (puppet *Puppet) ProcessResponse(resp *mautrix.RespSync, _ string) error {
	if !puppet.customUser.IsConnected() {
		puppet.log.Debugln("Skipping sync processing: custom user not connected to whatsapp")
		return nil
	}
	for roomID, events := range resp.Rooms.Join {
		portal := puppet.bridge.GetPortalByMXID(roomID)
		if portal == nil || portal.IsBroadcastList() {
			continue
		}
		for _, evt := range events.Ephemeral.Events {
			err := evt.Content.ParseRaw(evt.Type)
			if err != nil {
				continue
			}
			switch evt.Type {
			case event.EphemeralEventReceipt:
				if puppet.EnableReceipts {
					go puppet.handleReceiptEvent(portal, evt)
				}
			case event.EphemeralEventTyping:
				go puppet.handleTypingEvent(portal, evt)
			}
		}
	}
	if puppet.EnablePresence {
		for _, evt := range resp.Presence.Events {
			if evt.Sender != puppet.CustomMXID {
				continue
			}
			err := evt.Content.ParseRaw(evt.Type)
			if err != nil {
				continue
			}
			go puppet.handlePresenceEvent(evt)
		}
	}
	return nil
}

func (puppet *Puppet) handlePresenceEvent(event *event.Event) {
	presence := whatsapp.PresenceAvailable
	if event.Content.Raw["presence"].(string) != "online" {
		presence = whatsapp.PresenceUnavailable
		puppet.customUser.log.Debugln("Marking offline")
	} else {
		puppet.customUser.log.Debugln("Marking online")
	}
	_, err := puppet.customUser.Conn.Presence("", presence)
	if err != nil {
		puppet.customUser.log.Warnln("Failed to set presence:", err)
	}
}

func (puppet *Puppet) handleReceiptEvent(portal *Portal, event *event.Event) {
	for eventID, receipts := range *event.Content.AsReceipt() {
		if receipt, ok := receipts.Read[puppet.CustomMXID]; !ok {
			// Ignore receipt events where this user isn't present.
		} else if isDoublePuppeted, _ := receipt.Extra["net.maunium.whatsapp.puppet"].(bool); isDoublePuppeted {
			puppet.customUser.log.Debugfln("Ignoring double puppeted read receipt %+v", event.Content.Raw)
			// Ignore double puppeted read receipts.
		} else if message := puppet.bridge.DB.Message.GetByMXID(eventID); message != nil {
			puppet.customUser.log.Debugfln("Marking %s/%s in %s/%s as read", message.JID, message.MXID, portal.Key.JID, portal.MXID)
			_, err := puppet.customUser.Conn.Read(portal.Key.JID, message.JID)
			if err != nil {
				puppet.customUser.log.Warnln("Error marking read:", err)
			}
		}
	}
}

func (puppet *Puppet) handleTypingEvent(portal *Portal, evt *event.Event) {
	isTyping := false
	for _, userID := range evt.Content.AsTyping().UserIDs {
		if userID == puppet.CustomMXID {
			isTyping = true
			break
		}
	}
	if puppet.customTypingIn[evt.RoomID] != isTyping {
		puppet.customTypingIn[evt.RoomID] = isTyping
		presence := whatsapp.PresenceComposing
		if !isTyping {
			puppet.customUser.log.Debugfln("Marking not typing in %s/%s", portal.Key.JID, portal.MXID)
			presence = whatsapp.PresencePaused
		} else {
			puppet.customUser.log.Debugfln("Marking typing in %s/%s", portal.Key.JID, portal.MXID)
		}
		_, err := puppet.customUser.Conn.Presence(portal.Key.JID, presence)
		if err != nil {
			puppet.customUser.log.Warnln("Error setting typing:", err)
		}
	}
}

func (puppet *Puppet) tryRelogin(cause error, action string) bool {
	if !puppet.bridge.Config.CanDoublePuppet(puppet.CustomMXID) {
		return false
	}
	puppet.log.Debugfln("Trying to relogin after '%v' while %s", cause, action)
	accessToken, err := puppet.loginWithSharedSecret(puppet.CustomMXID)
	if err != nil {
		puppet.log.Errorfln("Failed to relogin after '%v' while %s: %v", cause, action, err)
		return false
	}
	puppet.log.Infofln("Successfully relogined after '%v' while %s", cause, action)
	puppet.AccessToken = accessToken
	return true
}

func (puppet *Puppet) OnFailedSync(_ *mautrix.RespSync, err error) (time.Duration, error) {
	puppet.log.Warnln("Sync error:", err)
	if errors.Is(err, mautrix.MUnknownToken) {
		if !puppet.tryRelogin(err, "syncing") {
			return 0, err
		}
		puppet.customIntent.AccessToken = puppet.AccessToken
		return 0, nil
	}
	return 10 * time.Second, nil
}

func (puppet *Puppet) GetFilterJSON(_ id.UserID) *mautrix.Filter {
	everything := []event.Type{{Type: "*"}}
	return &mautrix.Filter{
		Presence: mautrix.FilterPart{
			Senders: []id.UserID{puppet.CustomMXID},
			Types:   []event.Type{event.EphemeralEventPresence},
		},
		AccountData: mautrix.FilterPart{NotTypes: everything},
		Room: mautrix.RoomFilter{
			Ephemeral:    mautrix.FilterPart{Types: []event.Type{event.EphemeralEventTyping, event.EphemeralEventReceipt}},
			IncludeLeave: false,
			AccountData:  mautrix.FilterPart{NotTypes: everything},
			State:        mautrix.FilterPart{NotTypes: everything},
			Timeline:     mautrix.FilterPart{NotTypes: everything},
		},
	}
}

func (puppet *Puppet) SaveFilterID(_ id.UserID, _ string)    {}
func (puppet *Puppet) SaveNextBatch(_ id.UserID, nbt string) { puppet.NextBatch = nbt; puppet.Update() }
func (puppet *Puppet) SaveRoom(_ *mautrix.Room)              {}
func (puppet *Puppet) LoadFilterID(_ id.UserID) string       { return "" }
func (puppet *Puppet) LoadNextBatch(_ id.UserID) string      { return puppet.NextBatch }
func (puppet *Puppet) LoadRoom(_ id.RoomID) *mautrix.Room    { return nil }
