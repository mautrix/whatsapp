package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

const (
	WANotLoggedIn status.BridgeStateErrorCode = "wa-not-logged-in"
)

type respGetProxy struct {
	ProxyURL string `json:"proxy_url"`
}

func (wa *WhatsAppConnector) getProxy(reason string) (string, error) {
	if wa.Config.GetProxyURL == "" {
		return wa.Config.Proxy, nil
	}
	parsed, err := url.Parse(wa.Config.GetProxyURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse address: %w", err)
	}
	q := parsed.Query()
	q.Set("reason", reason)
	parsed.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to prepare request: %w", err)
	}
	req.Header.Set("User-Agent", mautrix.DefaultUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	} else if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return "", fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	var respData respGetProxy
	err = json.NewDecoder(resp.Body).Decode(&respData)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	return respData.ProxyURL, nil
}

func (wa *WhatsAppConnector) updateProxy(client *whatsmeow.Client, isLogin bool) error {
	if wa.Config.ProxyOnlyLogin && !isLogin {
		return nil
	}
	reason := "connect"
	if isLogin {
		reason = "login"
	}
	if proxy, err := wa.getProxy(reason); err != nil {
		return fmt.Errorf("failed to get proxy address: %w", err)
	} else if err = client.SetProxyAddress(proxy); err != nil {
		return fmt.Errorf("failed to set proxy address: %w", err)
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

var (
	_ bridgev2.NetworkAPI = (*WhatsAppClient)(nil)
	//_ bridgev2.TypingHandlingNetworkAPI      = (*WhatsAppClient)(nil)
	//_ bridgev2.IdentifierResolvingNetworkAPI = (*WhatsAppClient)(nil)
	//_ bridgev2.GroupCreatingNetworkAPI       = (*WhatsAppClient)(nil)
	//_ bridgev2.ContactListingNetworkAPI      = (*WhatsAppClient)(nil)
	//_ bridgev2.RoomNameHandlingNetworkAPI    = (*WhatsAppClient)(nil)
	//_ bridgev2.RoomAvatarHandlingNetworkAPI  = (*WhatsAppClient)(nil)
	//_ bridgev2.RoomTopicHandlingNetworkAPI   = (*WhatsAppClient)(nil)
)

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
	if err := wa.Main.updateProxy(wa.Client, false); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to update proxy")
	}
	return wa.Client.Connect()
}

func (wa *WhatsAppClient) Disconnect() {
	wa.Client.Disconnect()
}

func (wa *WhatsAppClient) LogoutRemote(ctx context.Context) {
	if wa.Client == nil {
		return
	}
	err := wa.Client.Logout()
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to log out")
	}
}

func (wa *WhatsAppClient) IsLoggedIn() bool {
	return wa.Client != nil && wa.Client.IsLoggedIn()
}
