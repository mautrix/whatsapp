// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"go.mau.fi/whatsmeow"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
)

type ProvisioningAPI struct {
	bridge *Bridge
	log    log.Logger
}

func (prov *ProvisioningAPI) Init() {
	prov.log = prov.bridge.Log.Sub("Provisioning")
	prov.log.Debugln("Enabling provisioning API at", prov.bridge.Config.AppService.Provisioning.Prefix)
	r := prov.bridge.AS.Router.PathPrefix(prov.bridge.Config.AppService.Provisioning.Prefix).Subrouter()
	r.Use(prov.AuthMiddleware)
	r.HandleFunc("/ping", prov.Ping).Methods(http.MethodGet)
	r.HandleFunc("/login", prov.Login).Methods(http.MethodGet)
	r.HandleFunc("/logout", prov.Logout).Methods(http.MethodPost)
	r.HandleFunc("/delete_session", prov.DeleteSession).Methods(http.MethodPost)
	r.HandleFunc("/disconnect", prov.Disconnect).Methods(http.MethodPost)
	r.HandleFunc("/reconnect", prov.Reconnect).Methods(http.MethodPost)
	prov.bridge.AS.Router.HandleFunc("/_matrix/app/com.beeper.asmux/ping", prov.BridgeStatePing).Methods(http.MethodPost)
	prov.bridge.AS.Router.HandleFunc("/_matrix/app/com.beeper.bridge_state", prov.BridgeStatePing).Methods(http.MethodPost)

	// Deprecated, just use /disconnect
	r.HandleFunc("/delete_connection", prov.Disconnect).Methods(http.MethodPost)
}

type responseWrap struct {
	http.ResponseWriter
	statusCode int
}

var _ http.Hijacker = (*responseWrap)(nil)

func (rw *responseWrap) WriteHeader(statusCode int) {
	rw.ResponseWriter.WriteHeader(statusCode)
	rw.statusCode = statusCode
}

func (rw *responseWrap) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

func (prov *ProvisioningAPI) AuthMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if len(auth) == 0 && strings.HasSuffix(r.URL.Path, "/login") {
			authParts := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
			for _, part := range authParts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "net.maunium.whatsapp.auth-") {
					auth = part[len("net.maunium.whatsapp.auth-"):]
					break
				}
			}
		} else if strings.HasPrefix(auth, "Bearer ") {
			auth = auth[len("Bearer "):]
		}
		if auth != prov.bridge.Config.AppService.Provisioning.SharedSecret {
			jsonResponse(w, http.StatusForbidden, map[string]interface{}{
				"error":   "Invalid auth token",
				"errcode": "M_FORBIDDEN",
			})
			return
		}
		userID := r.URL.Query().Get("user_id")
		user := prov.bridge.GetUserByMXID(id.UserID(userID))
		start := time.Now()
		wWrap := &responseWrap{w, 200}
		h.ServeHTTP(wWrap, r.WithContext(context.WithValue(r.Context(), "user", user)))
		duration := time.Now().Sub(start).Seconds()
		prov.log.Infofln("%s %s from %s took %.2f seconds and returned status %d", r.Method, r.URL.Path, user.MXID, duration, wWrap.statusCode)
	})
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

func (prov *ProvisioningAPI) DeleteSession(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Session == nil && user.Client == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "Nothing to purge: no session information stored and no active connection.",
			ErrCode: "no session",
		})
		return
	}
	user.DeleteConnection()
	user.DeleteSession()
	jsonResponse(w, http.StatusOK, Response{true, "Session information purged"})
	user.removeFromJIDMap(StateLoggedOut)
}

func (prov *ProvisioningAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Client == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "You don't have a WhatsApp connection.",
			ErrCode: "no connection",
		})
		return
	}
	user.DeleteConnection()
	jsonResponse(w, http.StatusOK, Response{true, "Disconnected from WhatsApp"})
	user.sendBridgeState(BridgeState{StateEvent: StateBadCredentials, Error: WANotConnected})
}

func (prov *ProvisioningAPI) Reconnect(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Client == nil {
		if user.Session == nil {
			jsonResponse(w, http.StatusForbidden, Error{
				Error:   "No existing connection and no session. Please log in first.",
				ErrCode: "no session",
			})
		} else {
			user.Connect()
			jsonResponse(w, http.StatusAccepted, Response{true, "Created connection to WhatsApp."})
		}
	} else {
		user.DeleteConnection()
		user.sendBridgeState(BridgeState{StateEvent: StateTransientDisconnect, Error: WANotConnected})
		user.Connect()
		jsonResponse(w, http.StatusAccepted, Response{true, "Restarted connection to WhatsApp"})
	}
}

func (prov *ProvisioningAPI) Ping(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	wa := map[string]interface{}{
		"has_session":     user.Session != nil,
		"management_room": user.ManagementRoom,
		"conn":            nil,
	}
	if !user.JID.IsEmpty() {
		wa["jid"] = user.JID.String()
		wa["phone"] = "+" + user.JID.User
		wa["device"] = user.JID.Device
	}
	if user.Client != nil {
		wa["conn"] = map[string]interface{}{
			"is_connected": user.Client.IsConnected(),
			"is_logged_in": user.Client.IsLoggedIn,
		}
	}
	resp := map[string]interface{}{
		"mxid":              user.MXID,
		"admin":             user.Admin,
		"whitelisted":       user.Whitelisted,
		"relay_whitelisted": user.RelayWhitelisted,
		"whatsapp":          wa,
	}
	jsonResponse(w, http.StatusOK, resp)
}

func jsonResponse(w http.ResponseWriter, status int, response interface{}) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func (prov *ProvisioningAPI) Logout(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Session == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "You're not logged in",
			ErrCode: "not logged in",
		})
		return
	}

	force := strings.ToLower(r.URL.Query().Get("force")) != "false"

	if user.Client == nil {
		if !force {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "You're not connected",
				ErrCode: "not connected",
			})
		}
	} else {
		err := user.Client.Logout()
		if err != nil {
			user.log.Warnln("Error while logging out:", err)
			if !force {
				jsonResponse(w, http.StatusInternalServerError, Error{
					Error:   fmt.Sprintf("Unknown error while logging out: %v", err),
					ErrCode: err.Error(),
				})
				return
			}
		} else {
			user.Session = nil
		}
		user.DeleteConnection()
	}

	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	user.removeFromJIDMap(StateLoggedOut)
	user.DeleteSession()
	jsonResponse(w, http.StatusOK, Response{true, "Logged out successfully."})
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	Subprotocols: []string{"net.maunium.whatsapp.login"},
}

func (prov *ProvisioningAPI) Login(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	user := prov.bridge.GetUserByMXID(id.UserID(userID))

	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		prov.log.Errorln("Failed to upgrade connection to websocket:", err)
		return
	}
	defer func() {
		err := c.Close()
		if err != nil {
			user.log.Debugln("Error closing websocket:", err)
		}
	}()

	go func() {
		// Read everything so SetCloseHandler() works
		for {
			_, _, err = c.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	c.SetCloseHandler(func(code int, text string) error {
		user.log.Debugfln("Login websocket closed (%d), cancelling login", code)
		cancel()
		return nil
	})

	qrChan, err := user.Login(ctx)
	if err != nil {
		user.log.Errorf("Failed to log in from provisioning API:", err)
		if errors.Is(err, ErrAlreadyLoggedIn) {
			go user.Connect()
			_ = c.WriteJSON(Error{
				Error:   "You're already logged into WhatsApp",
				ErrCode: "already logged in",
			})
		} else {
			_ = c.WriteJSON(Error{
				Error:   "Failed to connect to WhatsApp",
				ErrCode: "connection error",
			})
		}
	}
	user.log.Debugln("Started login via provisioning API")

	for {
		select {
		case evt := <-qrChan:
			switch evt {
			case whatsmeow.QRChannelSuccess:
				jid := user.Client.Store.ID
				user.log.Debugln("Successful login as", jid, "via provisioning API")
				_ = c.WriteJSON(map[string]interface{}{
					"success": true,
					"jid":     jid,
					"phone":   fmt.Sprintf("+%s", jid.User),
				})
			case whatsmeow.QRChannelTimeout:
				user.log.Debugln("Login via provisioning API timed out")
				_ = c.WriteJSON(Error{
					Error:   "QR code scan timed out. Please try again.",
					ErrCode: "login timed out",
				})
			case whatsmeow.QRChannelErrUnexpectedEvent:
				user.log.Debugln("Login via provisioning API failed due to unexpected event")
				_ = c.WriteJSON(Error{
					Error:   "Got unexpected event while waiting for QRs, perhaps you're already logged in?",
					ErrCode: "unexpected event",
				})
			case whatsmeow.QRChannelScannedWithoutMultidevice:
				_ = c.WriteJSON(Error{
					Error:   "Please enable the WhatsApp multidevice beta and scan the QR code again.",
					ErrCode: "multidevice not enabled",
				})
				continue
			default:
				_ = c.WriteJSON(map[string]interface{}{
					"code":    evt.Code,
					"timeout": int(evt.Timeout.Seconds()),
				})
				continue
			}
			return
		case <-ctx.Done():
			return
		}
	}
}
