package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppConnector) LoadUserLogin(_ context.Context, login *bridgev2.UserLogin) error {
	w := &WhatsAppClient{
		Main:      wa,
		UserLogin: login,
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
		w.Client.AutomaticMessageRerequestFromPhone = true
		w.Client.SetForceActiveDeliveryReceipts(wa.Config.ForceActiveDeliveryReceipts)
	} else {
		w.UserLogin.Log.Warn().Stringer("jid", w.JID).Msg("No device found for user in whatsmeow store")
	}

	return nil
}

type WhatsAppClient struct {
	Main      *WhatsAppConnector
	UserLogin *bridgev2.UserLogin
	Client    *whatsmeow.Client
	Device    *store.Device
	JID       types.JID
}

var _ bridgev2.NetworkAPI = (*WhatsAppClient)(nil)

var pushCfg = &bridgev2.PushConfig{
	Web: &bridgev2.WebPushConfig{},
}

func (wa *WhatsAppClient) GetPushConfigs() *bridgev2.PushConfig {
	// implement get application server key (to be added to whatsmeow)
	//pushCfg.Web.VapidKey = applicationServerKey
	return pushCfg
}

func (wa *WhatsAppClient) RegisterPushNotifications(_ context.Context, pushType bridgev2.PushType, _ string) error {
	if wa.Client == nil {
		return bridgev2.ErrNotLoggedIn
	}
	if pushType != bridgev2.PushTypeWeb {
		return fmt.Errorf("unsupported push type: %s", pushType)
	}

	//wa.Client.RegisterWebPush(ctx, token) (to be added to whatsmeow)
	return nil
}

func (wa *WhatsAppClient) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return userID == waid.MakeUserID(wa.JID)
}

func (wa *WhatsAppClient) Connect(ctx context.Context) error {
	if wa.Client == nil {
		state := status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      WANotLoggedIn,
		}
		wa.UserLogin.BridgeState.Send(state)
		return nil
	}
	if err := wa.Main.updateProxy(ctx, wa.Client, false); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to update proxy")
	}
	return wa.Client.Connect()
}

func (wa *WhatsAppClient) Disconnect() {
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
