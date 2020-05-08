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

// +build cgo

package main

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"time"

	"github.com/pkg/errors"
	"maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var levelTrace = maulogger.Level{
	Name:     "Trace",
	Severity: -10,
	Color:    -1,
}

type CryptoHelper struct {
	bridge *Bridge
	client *mautrix.Client
	mach   *crypto.OlmMachine
	log    maulogger.Logger
}

func (bridge *Bridge) initCrypto() error {
	if !bridge.Config.Bridge.Encryption.Allow {
		bridge.Log.Debugln("Bridge built with end-to-bridge encryption, but disabled in config")
		return nil
	} else if bridge.Config.Bridge.LoginSharedSecret == "" {
		bridge.Log.Warnln("End-to-bridge encryption enabled, but login_shared_secret not set")
		return nil
	}
	bridge.Log.Debugln("Initializing end-to-bridge encryption...")
	client, err := bridge.loginBot()
	if err != nil {
		return err
	}
	// TODO put this in the database
	cryptoStore, err := crypto.NewGobStore("crypto.gob")
	if err != nil {
		return err
	}

	log := bridge.Log.Sub("Crypto")
	logger := &cryptoLogger{log}
	stateStore := &cryptoStateStore{bridge}
	helper := &CryptoHelper{
		bridge: bridge,
		client: client,
		log: log.Sub("Helper"),
		mach: crypto.NewOlmMachine(client, logger, cryptoStore, stateStore),
	}

	client.Logger = logger.int.Sub("Bot")
	client.Syncer = &cryptoSyncer{helper.mach}
	// TODO put this in the database too
	client.Store = mautrix.NewInMemoryStore()

	err = helper.mach.Load()
	if err != nil {
		return err
	}

	bridge.Crypto = helper
	return nil
}

func (helper *CryptoHelper) Start() {
	helper.log.Debugln("Starting syncer for receiving to-device messages")
	err := helper.client.Sync()
	if err != nil {
		helper.log.Errorln("Fatal error syncing:", err)
	}
}

func (helper *CryptoHelper) Stop() {
	helper.client.StopSync()
}

func (bridge *Bridge) loginBot() (*mautrix.Client, error) {
	mac := hmac.New(sha512.New, []byte(bridge.Config.Bridge.LoginSharedSecret))
	mac.Write([]byte(bridge.AS.BotMXID()))
	resp, err := bridge.AS.BotClient().Login(&mautrix.ReqLogin{
		Type:                     "m.login.password",
		Identifier:               mautrix.UserIdentifier{Type: "m.id.user", User: string(bridge.AS.BotMXID())},
		Password:                 hex.EncodeToString(mac.Sum(nil)),
		DeviceID:                 "WhatsApp Bridge",
		InitialDeviceDisplayName: "WhatsApp Bridge",
	})
	if err != nil {
		return nil, err
	}
	client, err := mautrix.NewClient(bridge.AS.HomeserverURL, bridge.AS.BotMXID(), resp.AccessToken)
	if err != nil {
		return nil, err
	}
	client.DeviceID = "WhatsApp Bridge"
	return client, nil
}

func (helper *CryptoHelper) Decrypt(evt *event.Event) (*event.Event, error) {
	return helper.mach.DecryptMegolmEvent(evt)
}

func (helper *CryptoHelper) Encrypt(roomID id.RoomID, evtType event.Type, content event.Content) (*event.EncryptedEventContent, error) {
	encrypted, err := helper.mach.EncryptMegolmEvent(roomID, evtType, content)
	if err != nil {
		if err != crypto.SessionExpired && err != crypto.SessionNotShared && err != crypto.NoGroupSession {
			return nil, err
		}
		helper.log.Debugfln("Got %v while encrypting event for %s, sharing group session and trying again...", err, roomID)
		users, err := helper.bridge.StateStore.GetRoomMemberList(roomID)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get room member list")
		}
		err = helper.mach.ShareGroupSession(roomID, users)
		if err != nil {
			return nil, errors.Wrap(err, "failed to share group session")
		}
		encrypted, err = helper.mach.EncryptMegolmEvent(roomID, evtType, content)
		if err != nil {
			return nil, errors.Wrap(err, "failed to encrypt event after re-sharing group session")
		}
	}
	return encrypted, nil
}

func (helper *CryptoHelper) HandleMemberEvent(evt *event.Event) {
	helper.mach.HandleMemberEvent(evt)
}

type cryptoSyncer struct {
	*crypto.OlmMachine
}

func (syncer *cryptoSyncer) ProcessResponse(resp *mautrix.RespSync, since string) error {
	syncer.ProcessSyncResponse(resp, since)
	return nil
}

func (syncer *cryptoSyncer) OnFailedSync(_ *mautrix.RespSync, err error) (time.Duration, error) {
	syncer.Log.Error("Error /syncing, waiting 10 seconds: %v", err)
	return 10 * time.Second, nil
}

func (syncer *cryptoSyncer) GetFilterJSON(_ id.UserID) *mautrix.Filter {
	everything := []event.Type{{Type: "*"}}
	return &mautrix.Filter{
		Presence:    mautrix.FilterPart{NotTypes: everything},
		AccountData: mautrix.FilterPart{NotTypes: everything},
		Room: mautrix.RoomFilter{
			IncludeLeave: false,
			Ephemeral:    mautrix.FilterPart{NotTypes: everything},
			AccountData:  mautrix.FilterPart{NotTypes: everything},
			State:        mautrix.FilterPart{NotTypes: everything},
			Timeline:     mautrix.FilterPart{NotTypes: everything},
		},
	}
}

type cryptoLogger struct {
	int maulogger.Logger
}

func (c *cryptoLogger) Error(message string, args ...interface{}) {
	c.int.Errorfln(message, args...)
}

func (c *cryptoLogger) Warn(message string, args ...interface{}) {
	c.int.Warnfln(message, args...)
}

func (c *cryptoLogger) Debug(message string, args ...interface{}) {
	c.int.Debugfln(message, args...)
}

func (c *cryptoLogger) Trace(message string, args ...interface{}) {
	c.int.Logfln(levelTrace, message, args...)
}

type cryptoStateStore struct {
	bridge *Bridge
}

func (c *cryptoStateStore) IsEncrypted(id id.RoomID) bool {
	portal := c.bridge.GetPortalByMXID(id)
	if portal != nil {
		return portal.Encrypted
	}
	return false
}

func (c *cryptoStateStore) FindSharedRooms(id id.UserID) []id.RoomID {
	return c.bridge.StateStore.FindSharedRooms(id)
}
