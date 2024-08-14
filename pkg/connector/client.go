package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

type respGetProxy struct {
	ProxyURL string `json:"proxy_url"`
}

func (wa *WhatsAppClient) getProxy(reason string) (string, error) {
	if wa.Main.Config.GetProxyURL == "" {
		return wa.Main.Config.Proxy, nil
	}
	parsed, err := url.Parse(wa.Main.Config.GetProxyURL)
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

func (wa *WhatsAppClient) MakeNewClient() {
	wa.Client = whatsmeow.NewClient(wa.Device, waLog.Zerolog(wa.UserLogin.Log.With().Str("component", "whatsmeow").Logger()))

	if wa.Client.Store.ID != nil {
		wa.Client.EnableAutoReconnect = true
	} else {
		wa.Client.EnableAutoReconnect = false // no auto reconnect unless we are logged in
	}

	wa.Client.AutomaticMessageRerequestFromPhone = true
	wa.Client.SetForceActiveDeliveryReceipts(wa.Main.Config.ForceActiveDeliveryReceipts)

	wa.Client.AddEventHandler(wa.handleWAEvent)

	//TODO: add tracking under PreRetryCallback and GetMessageForRetry (waiting till metrics/analytics are added)
	if wa.Main.Config.ProxyOnlyLogin || wa.Device.ID == nil {
		if proxy, err := wa.getProxy("login"); err != nil {
			wa.UserLogin.Log.Err(err).Msg("Failed to get proxy address")
		} else if err = wa.Client.SetProxyAddress(proxy, whatsmeow.SetProxyOptions{
			NoMedia: wa.Main.Config.ProxyOnlyLogin,
		}); err != nil {
			wa.UserLogin.Log.Err(err).Msg("Failed to set proxy address")
		}
	}
	if wa.Main.Config.ProxyOnlyLogin {
		wa.Client.ToggleProxyOnlyForLogin(true)
	}

}

func (wa *WhatsAppClient) messageIDToKey(id *waid.ParsedMessageID) *waCommon.MessageKey {
	key := &waCommon.MessageKey{
		RemoteJID: ptr.Ptr(id.Chat.String()),
		ID:        ptr.Ptr(id.ID),
	}
	if id.Sender.User == string(wa.UserLogin.ID) {
		key.FromMe = ptr.Ptr(true)
	}
	if id.Chat.Server != types.MessengerServer && id.Chat.Server != types.DefaultUserServer {
		key.Participant = ptr.Ptr(id.Sender.String())
	}
	return key
}

func (wa *WhatsAppClient) keyToMessageID(chat, sender types.JID, key *waCommon.MessageKey) networkid.MessageID {
	sender = sender.ToNonAD()
	var err error
	if !key.GetFromMe() {
		if key.GetParticipant() != "" {
			sender, err = types.ParseJID(key.GetParticipant())
			if err != nil {
				// TODO log somehow?
				return ""
			}
			if sender.Server == types.LegacyUserServer {
				sender.Server = types.DefaultUserServer
			}
		} else if chat.Server == types.DefaultUserServer {
			ownID := ptr.Val(wa.Device.ID).ToNonAD()
			if sender.User == ownID.User {
				sender = chat
			} else {
				sender = ownID
			}
		} else {
			// TODO log somehow?
			return ""
		}
	}
	remoteJID, err := types.ParseJID(key.GetRemoteJID())
	if err == nil && !remoteJID.IsEmpty() {
		// TODO use remote jid in other cases?
		if remoteJID.Server == types.GroupServer {
			chat = remoteJID
		}
	}
	return waid.MakeMessageID(chat, sender, key.GetID())
}

type WhatsAppClient struct {
	Main      *WhatsAppConnector
	UserLogin *bridgev2.UserLogin
	Client    *whatsmeow.Client
	Device    *store.Device

	State status.BridgeState
}

var whatsappCaps = &bridgev2.NetworkRoomCapabilities{
	FormattedText:    true,
	UserMentions:     true,
	LocationMessages: true,
	Captions:         true,
	Replies:          true,
	Edits:            true,
	EditMaxCount:     10,
	EditMaxAge:       15 * time.Minute,
	Deletes:          true,
	DeleteMaxAge:     48 * time.Hour,
	DefaultFileRestriction: &bridgev2.FileRestriction{
		// 100MB is the limit for videos by default.
		// HQ on images and videos can be enabled
		// Documents can do 2GB, TODO implementation
		MaxSize: 100 * 1024 * 1024,
	},
	//TODO: implement
	Files:         map[event.MessageType]bridgev2.FileRestriction{},
	ReadReceipts:  true,
	Reactions:     true,
	ReactionCount: 1,
}

func (wa *WhatsAppClient) GetCapabilities(_ context.Context, portal *bridgev2.Portal) *bridgev2.NetworkRoomCapabilities {
	if portal.Receiver == wa.UserLogin.ID && portal.ID == networkid.PortalID(wa.UserLogin.ID) {
		// note to self mode, not implemented yet
		return nil
	}
	return whatsappCaps
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

func (wa *WhatsAppClient) LogoutRemote(ctx context.Context) {
	if wa.Client == nil {
		return
	}
	err := wa.Client.Logout()
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to log out")
	}
}

func (wa *WhatsAppClient) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	if wa.Client == nil {
		return false
	}
	return userID == networkid.UserID(wa.Client.Store.ID.User)
}

func (wa *WhatsAppClient) Connect(_ context.Context) error {
	return wa.Client.Connect()
}

func (wa *WhatsAppClient) Disconnect() {
	wa.Client.Disconnect()
}

func (wa *WhatsAppClient) IsLoggedIn() bool {
	if wa.Client == nil {
		return false
	}
	return wa.Client.IsLoggedIn()
}

func (wa *WhatsAppClient) FullReconnect() {
	panic("unimplemented")
}

func (wa *WhatsAppClient) canReconnect() bool { return false }

func (wa *WhatsAppClient) resetWADevice() {
	wa.Device = nil
	wa.UserLogin.Metadata.(*UserLoginMetadata).WADeviceID = 0
}
