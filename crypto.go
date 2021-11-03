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

//go:build cgo && !nocrypto

package main

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/lib/pq"

	"maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

var NoSessionFound = crypto.NoSessionFound

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

func init() {
	crypto.PostgresArrayWrapper = pq.Array
}

func NewCryptoHelper(bridge *Bridge) Crypto {
	if !bridge.Config.Bridge.Encryption.Allow {
		bridge.Log.Debugln("Bridge built with end-to-bridge encryption, but disabled in config")
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

	helper.store = database.NewSQLCryptoStore(helper.bridge.DB, helper.bridge.AS.BotMXID(),
		fmt.Sprintf("@%s:%s", helper.bridge.Config.Bridge.FormatUsername("%"), helper.bridge.AS.HomeserverDomain))

	var err error
	helper.client, err = helper.loginBot()
	if err != nil {
		return err
	}

	helper.log.Debugln("Logged in as bridge bot with device ID", helper.client.DeviceID)
	logger := &cryptoLogger{helper.baseLog}
	stateStore := &cryptoStateStore{helper.bridge}
	helper.mach = crypto.NewOlmMachine(helper.client, logger, helper.store, stateStore)
	helper.mach.AllowKeyShare = helper.allowKeyShare

	helper.client.Syncer = &cryptoSyncer{helper.mach}
	helper.client.Store = &cryptoClientStore{helper.store}

	return helper.mach.Load()
}

func (helper *CryptoHelper) allowKeyShare(device *crypto.DeviceIdentity, info event.RequestedKeyInfo) *crypto.KeyShareRejection {
	cfg := helper.bridge.Config.Bridge.Encryption.KeySharing
	if !cfg.Allow {
		return &crypto.KeyShareRejectNoResponse
	} else if device.Trust == crypto.TrustStateBlacklisted {
		return &crypto.KeyShareRejectBlacklisted
	} else if device.Trust == crypto.TrustStateVerified || !cfg.RequireVerification {
		portal := helper.bridge.GetPortalByMXID(info.RoomID)
		if portal == nil {
			helper.log.Debugfln("Rejecting key request for %s from %s/%s: room is not a portal", info.SessionID, device.UserID, device.DeviceID)
			return &crypto.KeyShareRejection{Code: event.RoomKeyWithheldUnavailable, Reason: "Requested room is not a portal room"}
		}
		user := helper.bridge.GetUserByMXID(device.UserID)
		// FIXME reimplement IsInPortal
		if !user.Admin /*&& !user.IsInPortal(portal.Key)*/ {
			helper.log.Debugfln("Rejecting key request for %s from %s/%s: user is not in portal", info.SessionID, device.UserID, device.DeviceID)
			return &crypto.KeyShareRejection{Code: event.RoomKeyWithheldUnauthorized, Reason: "You're not in that portal"}
		}
		helper.log.Debugfln("Accepting key request for %s from %s/%s", info.SessionID, device.UserID, device.DeviceID)
		return nil
	} else {
		return &crypto.KeyShareRejectUnverified
	}
}

func (helper *CryptoHelper) loginBot() (*mautrix.Client, error) {
	deviceID := helper.store.FindDeviceID()
	if len(deviceID) > 0 {
		helper.log.Debugln("Found existing device ID for bot in database:", deviceID)
	}
	client, err := mautrix.NewClient(helper.bridge.AS.HomeserverURL, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize client: %w", err)
	}
	client.Logger = helper.baseLog.Sub("Bot")
	client.Client = helper.bridge.AS.HTTPClient
	client.DefaultHTTPRetries = helper.bridge.AS.DefaultHTTPRetries
	flows, err := client.GetLoginFlows()
	if err != nil {
		return nil, fmt.Errorf("failed to get supported login flows: %w", err)
	}
	if !flows.HasFlow(mautrix.AuthTypeHalfyAppservice) {
		return nil, fmt.Errorf("homeserver does not support appservice login")
	}
	// We set the API token to the AS token here to authenticate the appservice login
	// It'll get overridden after the login
	client.AccessToken = helper.bridge.AS.Registration.AppToken
	resp, err := client.Login(&mautrix.ReqLogin{
		Type:                     mautrix.AuthTypeHalfyAppservice,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: string(helper.bridge.AS.BotMXID())},
		DeviceID:                 deviceID,
		InitialDeviceDisplayName: "WhatsApp Bridge",
		StoreCredentials:         true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to log in as bridge bot: %w", err)
	}
	helper.store.DeviceID = resp.DeviceID
	return client, nil
}

func (helper *CryptoHelper) Start() {
	helper.log.Debugln("Starting syncer for receiving to-device messages")
	err := helper.client.Sync()
	if err != nil {
		helper.log.Errorln("Fatal error syncing:", err)
	} else {
		helper.log.Infoln("Bridge bot to-device syncer stopped without error")
	}
}

func (helper *CryptoHelper) Stop() {
	helper.log.Debugln("CryptoHelper.Stop() called, stopping bridge bot sync")
	helper.client.StopSync()
}

func (helper *CryptoHelper) Decrypt(evt *event.Event) (*event.Event, error) {
	return helper.mach.DecryptMegolmEvent(evt)
}

func (helper *CryptoHelper) Encrypt(roomID id.RoomID, evtType event.Type, content event.Content) (*event.EncryptedEventContent, error) {
	encrypted, err := helper.mach.EncryptMegolmEvent(roomID, evtType, &content)
	if err != nil {
		if err != crypto.SessionExpired && err != crypto.SessionNotShared && err != crypto.NoGroupSession {
			return nil, err
		}
		helper.log.Debugfln("Got %v while encrypting event for %s, sharing group session and trying again...", err, roomID)
		users, err := helper.store.GetRoomMembers(roomID)
		if err != nil {
			return nil, fmt.Errorf("failed to get room member list: %w", err)
		}
		err = helper.mach.ShareGroupSession(roomID, users)
		if err != nil {
			return nil, fmt.Errorf("failed to share group session: %w", err)
		}
		encrypted, err = helper.mach.EncryptMegolmEvent(roomID, evtType, &content)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt event after re-sharing group session: %w", err)
		}
	}
	return encrypted, nil
}

func (helper *CryptoHelper) WaitForSession(roomID id.RoomID, senderKey id.SenderKey, sessionID id.SessionID, timeout time.Duration) bool {
	return helper.mach.WaitForSession(roomID, senderKey, sessionID, timeout)
}

func (helper *CryptoHelper) ResetSession(roomID id.RoomID) {
	err := helper.mach.CryptoStore.RemoveOutboundGroupSession(roomID)
	if err != nil {
		helper.log.Debugfln("Error manually removing outbound group session in %s: %v", roomID, err)
	}
}

func (helper *CryptoHelper) HandleMemberEvent(evt *event.Event) {
	helper.mach.HandleMemberEvent(evt)
}

type cryptoSyncer struct {
	*crypto.OlmMachine
}

func (syncer *cryptoSyncer) ProcessResponse(resp *mautrix.RespSync, since string) error {
	done := make(chan struct{})
	go func() {
		defer func() {
			if err := recover(); err != nil {
				syncer.Log.Error("Processing sync response (%s) panicked: %v\n%s", since, err, debug.Stack())
			}
			done <- struct{}{}
		}()
		syncer.Log.Trace("Starting sync response handling (%s)", since)
		syncer.ProcessSyncResponse(resp, since)
		syncer.Log.Trace("Successfully handled sync response (%s)", since)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		syncer.Log.Warn("Handling sync response (%s) is taking unusually long", since)
	}
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

var _ crypto.StateStore = (*cryptoStateStore)(nil)

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

func (c *cryptoStateStore) GetEncryptionEvent(id.RoomID) *event.EncryptionEventContent {
	// TODO implement
	return nil
}
