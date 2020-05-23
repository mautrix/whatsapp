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
	"fmt"
	"time"

	"github.com/pkg/errors"
	"maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix-whatsapp/database"
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
	bridge  *Bridge
	client  *mautrix.Client
	mach    *crypto.OlmMachine
	store   *database.SQLCryptoStore
	log     maulogger.Logger
	baseLog maulogger.Logger
}

func NewCryptoHelper(bridge *Bridge) Crypto {
	if !bridge.Config.Bridge.Encryption.Allow {
		bridge.Log.Debugln("Bridge built with end-to-bridge encryption, but disabled in config")
		return nil
	} else if bridge.Config.Bridge.LoginSharedSecret == "" {
		bridge.Log.Warnln("End-to-bridge encryption enabled, but login_shared_secret not set")
		return nil
	}
	baseLog := bridge.Log.Sub("Crypto")
	return &CryptoHelper{
		bridge:  bridge,
		log:     baseLog.Sub("Helper"),
		baseLog: baseLog,
	}
}

func (helper *CryptoHelper) Init() error {
	helper.log.Debugln("Initializing end-to-bridge encryption...")
	var err error
	helper.client, err = helper.loginBot()
	if err != nil {
		return err
	}

	helper.log.Debugln("Logged in as bridge bot with device ID", helper.client.DeviceID)
	logger := &cryptoLogger{helper.baseLog}
	stateStore := &cryptoStateStore{helper.bridge}
	helper.store = database.NewSQLCryptoStore(helper.bridge.DB, helper.client.DeviceID)
	helper.store.UserID = helper.client.UserID
	helper.store.GhostIDFormat = fmt.Sprintf("@%s:%s", helper.bridge.Config.Bridge.FormatUsername("%"), helper.bridge.AS.HomeserverDomain)
	helper.mach = crypto.NewOlmMachine(helper.client, logger, helper.store, stateStore)

	helper.client.Logger = logger.int.Sub("Bot")
	helper.client.Syncer = &cryptoSyncer{helper.mach}
	helper.client.Store = &cryptoClientStore{helper.store}

	return helper.mach.Load()
}

func (helper *CryptoHelper) loginBot() (*mautrix.Client, error) {
	deviceID := helper.bridge.DB.FindDeviceID()
	if len(deviceID) > 0 {
		helper.log.Debugln("Found existing device ID for bot in database:", deviceID)
	}
	mac := hmac.New(sha512.New, []byte(helper.bridge.Config.Bridge.LoginSharedSecret))
	mac.Write([]byte(helper.bridge.AS.BotMXID()))
	resp, err := helper.bridge.AS.BotClient().Login(&mautrix.ReqLogin{
		Type:                     "m.login.password",
		Identifier:               mautrix.UserIdentifier{Type: "m.id.user", User: string(helper.bridge.AS.BotMXID())},
		Password:                 hex.EncodeToString(mac.Sum(nil)),
		DeviceID:                 deviceID,
		InitialDeviceDisplayName: "WhatsApp Bridge",
	})
	if err != nil {
		return nil, err
	}
	client, err := mautrix.NewClient(helper.bridge.AS.HomeserverURL, helper.bridge.AS.BotMXID(), resp.AccessToken)
	if err != nil {
		return nil, err
	}
	client.DeviceID = resp.DeviceID
	return client, nil
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
		users, err := helper.store.GetRoomMembers(roomID)
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

type cryptoClientStore struct {
	int *database.SQLCryptoStore
}

func (c cryptoClientStore) SaveFilterID(_ id.UserID, _ string) {}
func (c cryptoClientStore) LoadFilterID(_ id.UserID) string    { return "" }
func (c cryptoClientStore) SaveRoom(_ *mautrix.Room)           {}
func (c cryptoClientStore) LoadRoom(_ id.RoomID) *mautrix.Room { return nil }

func (c cryptoClientStore) SaveNextBatch(_ id.UserID, nextBatchToken string) {
	c.int.PutNextBatch(nextBatchToken)
}

func (c cryptoClientStore) LoadNextBatch(_ id.UserID) string {
	return c.int.GetNextBatch()
}

var _ mautrix.Storer = (*cryptoClientStore)(nil)

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
