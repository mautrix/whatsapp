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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"golang.org/x/sync/semaphore"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

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
	Web: &bridgev2.WebPushConfig{VapidKey: "BIt4eFAVqVxe4yOA5_VLbZTbOlV-2y1FYJ_R4RlxWoyYazAq4glIxI7fh_xLbob1SNv7ZtTWn9mmZCsk2YNXYeY"},
	FCM: &bridgev2.FCMPushConfig{SenderID: "293955441834"},
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
			pn := gjson.GetBytes(params.RawData, "data.pn").Str
			if pn != "" {
				pnErr := wa.sendPNData(ctx, pn)
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

func (wa *WhatsAppClient) Disconnect() {
	if stopHistorySyncLoop := wa.stopLoops.Swap(nil); stopHistorySyncLoop != nil {
		(*stopHistorySyncLoop)()
	}
	if cli := wa.Client; cli != nil {
		cli.Disconnect()
		wa.Client = nil
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
}

func (wa *WhatsAppClient) IsLoggedIn() bool {
	return wa.Client != nil && wa.Client.IsLoggedIn()
}
