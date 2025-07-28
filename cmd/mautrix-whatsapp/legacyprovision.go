package main

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/hlog"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/connector"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	Subprotocols: []string{"net.maunium.whatsapp.login"},
}

func legacyProvAuth(r *http.Request) string {
	if !strings.HasSuffix(r.URL.Path, "/v1/login") {
		return ""
	}
	authParts := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
	for _, part := range authParts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "net.maunium.whatsapp.auth-") {
			return strings.TrimPrefix(part, "net.maunium.whatsapp.auth-")
		}
	}
	return ""
}

type OtherUserInfo struct {
	MXID   id.UserID           `json:"mxid"`
	JID    types.JID           `json:"jid"`
	Name   string              `json:"displayname"`
	Avatar id.ContentURIString `json:"avatar_url"`
}

type PortalInfo struct {
	RoomID      id.RoomID        `json:"room_id"`
	OtherUser   *OtherUserInfo   `json:"other_user,omitempty"`
	GroupInfo   *types.GroupInfo `json:"group_info,omitempty"`
	JustCreated bool             `json:"just_created"`
}

type Error struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	ErrCode string `json:"errcode"`
}

type Response struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
}

func respondWebsocketWithError(conn *websocket.Conn, err error, message string) {
	var mautrixRespErr mautrix.RespError
	var bv2RespErr bridgev2.RespError
	if errors.As(err, &bv2RespErr) {
		mautrixRespErr = mautrix.RespError(bv2RespErr)
	} else if !errors.As(err, &mautrixRespErr) {
		mautrixRespErr = mautrix.RespError{
			Err:        message,
			ErrCode:    "M_UNKNOWN",
			StatusCode: http.StatusInternalServerError,
		}
	}
	_ = conn.WriteJSON(&mautrixRespErr)
}

var notNumbers = regexp.MustCompile("[^0-9]")

func legacyProvLogin(w http.ResponseWriter, r *http.Request) {
	log := hlog.FromRequest(r)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Err(err).Msg("Failed to upgrade connection to websocket")
		return
	}
	defer func() {
		err := conn.Close()
		if err != nil {
			log.Debug().Err(err).Msg("Error closing websocket")
		}
	}()

	go func() {
		// Read everything so SetCloseHandler() works
		for {
			_, _, err = conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	conn.SetCloseHandler(func(code int, text string) error {
		log.Debug().Int("close_code", code).Msg("Login websocket closed, cancelling login")
		cancel()
		return nil
	})

	user := m.Matrix.Provisioning.GetUser(r)
	loginFlowID := connector.LoginFlowIDQR
	phoneNum := r.URL.Query().Get("phone_number")
	if phoneNum != "" {
		phoneNum = notNumbers.ReplaceAllString(phoneNum, "")
		if len(phoneNum) < 7 || strings.HasPrefix(phoneNum, "0") {
			errorMsg := "Invalid phone number"
			if len(phoneNum) > 6 {
				errorMsg = "Please enter the phone number in international format"
			}
			_ = conn.WriteJSON(Error{
				Error:   errorMsg,
				ErrCode: "invalid phone number",
			})
			return
		}
		loginFlowID = connector.LoginFlowIDPhone
	}
	login, err := c.CreateLogin(ctx, user, loginFlowID)
	if err != nil {
		log.Err(err).Msg("Failed to create login")
		respondWebsocketWithError(conn, err, "Failed to create login")
		return
	}
	waLogin := login.(*connector.WALogin)
	waLogin.Timezone = r.URL.Query().Get("tz")
	step, err := waLogin.Start(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to start login")
		respondWebsocketWithError(conn, err, "Failed to start login")
		return
	}
	if phoneNum != "" {
		if step.StepID != connector.LoginStepIDPhoneNumber {
			respondWebsocketWithError(conn, errors.New("unexpected step"), "Unexpected step while starting phone number login")
			waLogin.Cancel()
			return
		}
		step, err = waLogin.SubmitUserInput(ctx, map[string]string{"phone_number": phoneNum})
		if err != nil {
			log.Err(err).Msg("Failed to submit phone number")
			respondWebsocketWithError(conn, err, "Failed to start phone code login")
			return
		} else if step.StepID != connector.LoginStepIDCode {
			respondWebsocketWithError(conn, errors.New("unexpected step"), "Unexpected step after submitting phone number")
			waLogin.Cancel()
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"pairing_code": step.DisplayAndWaitParams.Data,
			"timeout":      180,
		})
	} else if step.StepID != connector.LoginStepIDQR {
		respondWebsocketWithError(conn, errors.New("unexpected step"), "Unexpected step while starting QR login")
		waLogin.Cancel()
		return
	} else {
		_ = conn.WriteJSON(map[string]any{
			"code":    step.DisplayAndWaitParams.Data,
			"timeout": 60,
		})
	}
	for {
		step, err = waLogin.Wait(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to wait for login")
			respondWebsocketWithError(conn, err, "Failed to wait for login")
		} else if step.StepID == connector.LoginStepIDQR {
			_ = conn.WriteJSON(map[string]any{
				"code":    step.DisplayAndWaitParams.Data,
				"timeout": 20,
			})
			continue
		} else if step.StepID != connector.LoginStepIDComplete {
			respondWebsocketWithError(conn, errors.New("unexpected step"), "Unexpected step while waiting for login")
			waLogin.Cancel()
		} else {
			// TODO delete old logins
			_ = conn.WriteJSON(map[string]any{
				"success":  true,
				"jid":      waid.ParseUserLoginID(step.CompleteParams.UserLoginID, step.CompleteParams.UserLogin.Metadata.(*waid.UserLoginMetadata).WADeviceID).String(),
				"platform": step.CompleteParams.UserLogin.Client.(*connector.WhatsAppClient).Device.Platform,
				"phone":    step.CompleteParams.UserLogin.RemoteProfile.Phone,
			})
			go handleLoginComplete(context.WithoutCancel(ctx), user, step.CompleteParams.UserLogin)
		}
		break
	}
}
func handleLoginComplete(ctx context.Context, user *bridgev2.User, newLogin *bridgev2.UserLogin) {
	allLogins := user.GetUserLogins()
	for _, login := range allLogins {
		if login.ID != newLogin.ID {
			login.Delete(ctx, status.BridgeState{StateEvent: status.StateLoggedOut, Reason: "LOGIN_OVERRIDDEN"}, bridgev2.DeleteOpts{})
		}
	}
}

func legacyProvLogout(w http.ResponseWriter, r *http.Request) {
	user := m.Matrix.Provisioning.GetUser(r)
	allLogins := user.GetUserLogins()
	if len(allLogins) == 0 {
		exhttp.WriteJSONResponse(w, http.StatusOK, Error{
			Error:   "You're not logged in",
			ErrCode: "not logged in",
		})
		return
	}
	for _, login := range allLogins {
		// Intentionally don't delete the user login, only logout remote
		login.Client.(*connector.WhatsAppClient).LogoutRemote(r.Context())
	}
	exhttp.WriteJSONResponse(w, http.StatusOK, Response{true, "Logged out successfully"})
}

func legacyProvContacts(w http.ResponseWriter, r *http.Request) {
	userLogin := m.Matrix.Provisioning.GetLoginForRequest(w, r)
	if userLogin == nil {
		return
	}
	if contacts, err := userLogin.Client.(*connector.WhatsAppClient).Device.Contacts.GetAllContacts(r.Context()); err != nil {
		hlog.FromRequest(r).Err(err).Msg("Failed to fetch all contacts")
		exhttp.WriteJSONResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal server error while fetching contact list",
			ErrCode: "failed to get contacts",
		})
	} else {
		augmentedContacts := map[types.JID]any{}
		for jid, contact := range contacts {
			var avatarURL id.ContentURIString
			if puppet, _ := m.Bridge.GetExistingGhostByID(r.Context(), waid.MakeUserID(jid)); puppet != nil {
				avatarURL = puppet.AvatarMXC
			}
			augmentedContacts[jid] = map[string]interface{}{
				"Found":        contact.Found,
				"FirstName":    contact.FirstName,
				"FullName":     contact.FullName,
				"PushName":     contact.PushName,
				"BusinessName": contact.BusinessName,
				"AvatarURL":    avatarURL,
			}
		}
		exhttp.WriteJSONResponse(w, http.StatusOK, augmentedContacts)
	}
}

func legacyProvResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	userLogin := m.Matrix.Provisioning.GetLoginForRequest(w, r)
	if userLogin == nil {
		return
	}
	startChat := strings.Contains(r.URL.Path, "/v1/pm/")
	resp, err := userLogin.Client.(*connector.WhatsAppClient).ResolveIdentifier(r.Context(), number, startChat)
	if err != nil {
		hlog.FromRequest(r).Warn().Err(err).Str("identifier", number).Msg("Failed to resolve identifier")
		matrix.RespondWithError(w, err, "Internal error resolving identifier")
		return
	}
	var portal *bridgev2.Portal
	if startChat {
		portal, err = m.Bridge.GetPortalByKey(r.Context(), resp.Chat.PortalKey)
		if err != nil {
			hlog.FromRequest(r).Warn().Err(err).Stringer("portal_key", resp.Chat.PortalKey).Msg("Failed to get portal by key")
			matrix.RespondWithError(w, err, "Internal error getting portal by key")
			return
		}
		err = portal.CreateMatrixRoom(r.Context(), userLogin, nil)
		if err != nil {
			hlog.FromRequest(r).Warn().Err(err).Stringer("portal_key", resp.Chat.PortalKey).Msg("Failed to create matrix room for portal")
			matrix.RespondWithError(w, err, "Internal error creating matrix room for portal")
			return
		}
	} else {
		portal, _ = m.Bridge.GetExistingPortalByKey(r.Context(), resp.Chat.PortalKey)
	}
	var roomID id.RoomID
	if portal != nil {
		roomID = portal.MXID
	}
	exhttp.WriteJSONResponse(w, http.StatusOK, PortalInfo{
		RoomID: roomID,
		OtherUser: &OtherUserInfo{
			JID:    waid.ParseUserID(resp.UserID),
			MXID:   resp.Ghost.Intent.GetMXID(),
			Name:   resp.Ghost.Name,
			Avatar: resp.Ghost.AvatarMXC,
		},
	})
}
