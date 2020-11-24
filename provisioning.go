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
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Rhymen/go-whatsapp"
	"github.com/gorilla/websocket"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"

	whatsappExt "maunium.net/go/mautrix-whatsapp/whatsapp-ext"
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
	r.HandleFunc("/login", prov.Login)
	r.HandleFunc("/logout", prov.Logout).Methods(http.MethodPost)
	r.HandleFunc("/delete_session", prov.DeleteSession).Methods(http.MethodPost)
	r.HandleFunc("/delete_connection", prov.DeleteConnection).Methods(http.MethodPost)
	r.HandleFunc("/disconnect", prov.Disconnect).Methods(http.MethodPost)
	r.HandleFunc("/reconnect", prov.Reconnect).Methods(http.MethodPost)
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
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), "user", user)))
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
	if user.Session == nil && user.Conn == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "Nothing to purge: no session information stored and no active connection.",
			ErrCode: "no session",
		})
		return
	}
	user.SetSession(nil)
	if user.Conn != nil {
		_, _ = user.Conn.Disconnect()
		user.Conn.RemoveHandlers()
		user.Conn = nil
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	}
	jsonResponse(w, http.StatusOK, Response{true, "Session information purged"})
}

func (prov *ProvisioningAPI) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Conn == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "You don't have a WhatsApp connection.",
			ErrCode: "not connected",
		})
		return
	}
	sess, err := user.Conn.Disconnect()
	if err == nil && len(sess.Wid) > 0 {
		user.SetSession(&sess)
	}
	user.Conn.RemoveHandlers()
	user.Conn = nil
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	jsonResponse(w, http.StatusOK, Response{true, "Disconnected from WhatsApp and connection deleted"})
}

func (prov *ProvisioningAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Conn == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "You don't have a WhatsApp connection.",
			ErrCode: "no connection",
		})
		return
	}
	sess, err := user.Conn.Disconnect()
	if err == whatsapp.ErrNotConnected {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "You were not connected",
			ErrCode: "not connected",
		})
		return
	} else if err != nil {
		user.log.Warnln("Error while disconnecting:", err)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   fmt.Sprintf("Unknown error while disconnecting: %v", err),
			ErrCode: err.Error(),
		})
		return
	} else if len(sess.Wid) > 0 {
		user.SetSession(&sess)
	}
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	jsonResponse(w, http.StatusOK, Response{true, "Disconnected from WhatsApp"})
}

func (prov *ProvisioningAPI) Reconnect(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user.Conn == nil {
		if user.Session == nil {
			jsonResponse(w, http.StatusForbidden, Error{
				Error:   "No existing connection and no session. Please log in first.",
				ErrCode: "no session",
			})
		} else {
			user.Connect(false)
			jsonResponse(w, http.StatusOK, Response{true, "Created connection to WhatsApp."})
		}
		return
	}

	wasConnected := true
	sess, err := user.Conn.Disconnect()
	if err == whatsapp.ErrNotConnected {
		wasConnected = false
	} else if err != nil {
		user.log.Warnln("Error while disconnecting:", err)
	} else if len(sess.Wid) > 0 {
		user.SetSession(&sess)
	}

	err = user.Conn.Restore()
	if err == whatsapp.ErrInvalidSession {
		if user.Session != nil {
			user.log.Debugln("Got invalid session error when reconnecting, but user has session. Retrying using RestoreWithSession()...")
			var sess whatsapp.Session
			sess, err = user.Conn.RestoreWithSession(*user.Session)
			if err == nil {
				user.SetSession(&sess)
			}
		} else {
			jsonResponse(w, http.StatusForbidden, Error{
				Error:   "You're not logged in",
				ErrCode: "not logged in",
			})
			return
		}
	} else if err == whatsapp.ErrLoginInProgress {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "A login or reconnection is already in progress.",
			ErrCode: "login in progress",
		})
		return
	} else if err == whatsapp.ErrAlreadyLoggedIn {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "You were already connected.",
			ErrCode: err.Error(),
		})
		return
	}
	if err != nil {
		user.log.Warnln("Error while reconnecting:", err)
		if err.Error() == "restore session connection timed out" {
			jsonResponse(w, http.StatusForbidden, Error{
				Error:   "Reconnection timed out. Is WhatsApp on your phone reachable?",
				ErrCode: err.Error(),
			})
		} else {
			jsonResponse(w, http.StatusForbidden, Error{
				Error:   fmt.Sprintf("Unknown error while reconnecting: %v", err),
				ErrCode: err.Error(),
			})
		}
		user.log.Debugln("Disconnecting due to failed session restore in reconnect command...")
		sess, err := user.Conn.Disconnect()
		if err != nil {
			user.log.Errorln("Failed to disconnect after failed session restore in reconnect command:", err)
		} else if len(sess.Wid) > 0 {
			user.SetSession(&sess)
		}
		return
	}
	user.ConnectionErrors = 0
	user.PostLogin()

	var msg string
	if wasConnected {
		msg = "Reconnected successfully."
	} else {
		msg = "Connected successfully."
	}

	jsonResponse(w, http.StatusOK, Response{true, msg})
}

func (prov *ProvisioningAPI) Ping(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	wa := map[string]interface{}{
		"has_session":     user.Session != nil,
		"management_room": user.ManagementRoom,
		"jid":             user.JID,
		"conn":            nil,
		"ping":            nil,
	}
	if user.Conn != nil {
		wa["conn"] = map[string]interface{}{
			"is_connected":         user.Conn.IsConnected(),
			"is_logged_in":         user.Conn.IsLoggedIn(),
			"is_login_in_progress": user.Conn.IsLoginInProgress(),
		}
		err := user.Conn.AdminTest()
		wa["ping"] = map[string]interface{}{
			"ok":  err == nil,
			"err": err,
		}
	}
	resp := map[string]interface{}{
		"mxid":                 user.MXID,
		"admin":                user.Admin,
		"whitelisted":          user.Whitelisted,
		"relaybot_whitelisted": user.RelaybotWhitelisted,
		"whatsapp":             wa,
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

	if user.Conn == nil {
		if !force {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "You're not connected",
				ErrCode: "not connected",
			})
		}
	} else {
		err := user.Conn.Logout()
		if err != nil {
			user.log.Warnln("Error while logging out:", err)
			if !force {
				jsonResponse(w, http.StatusInternalServerError, Error{
					Error:   fmt.Sprintf("Unknown error while logging out: %v", err),
					ErrCode: err.Error(),
				})
				return
			}
		}
		_, err = user.Conn.Disconnect()
		if err != nil {
			user.log.Warnln("Error while disconnecting after logout:", err)
		}
		user.Conn.RemoveHandlers()
		user.Conn = nil
	}

	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	user.removeFromJIDMap()

	// TODO this causes a foreign key violation, which should be fixed
	//ce.User.JID = ""
	user.SetSession(nil)
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
		prov.log.Errorfln("Failed to upgrade connection to websocket:", err)
		return
	}
	defer c.Close()

	if !user.Connect(true) {
		user.log.Debugln("Connect() returned false, assuming error was logged elsewhere and canceling login.")
		_ = c.WriteJSON(Error{
			Error:   "Failed to connect to WhatsApp",
			ErrCode: "connection error",
		})
		return
	}

	qrChan := make(chan string, 3)
	go func() {
		for code := range qrChan {
			if code == "stop" {
				return
			}
			_ = c.WriteJSON(map[string]interface{}{
				"code": code,
			})
		}
	}()
	session, err := user.Conn.LoginWithRetry(qrChan, user.bridge.Config.Bridge.LoginQRRegenCount)
	qrChan <- "stop"
	if err != nil {
		var msg string
		if err == whatsapp.ErrAlreadyLoggedIn {
			msg = "You're already logged in"
		} else if err == whatsapp.ErrLoginInProgress {
			msg = "You have a login in progress already."
		} else if err == whatsapp.ErrLoginTimedOut {
			msg = "QR code scan timed out. Please try again."
		} else {
			user.log.Warnln("Failed to log in:", err)
			msg = fmt.Sprintf("Unknown error while logging in: %v", err)
		}
		_ = c.WriteJSON(Error{
			Error:   msg,
			ErrCode: err.Error(),
		})
		return
	}
	user.ConnectionErrors = 0
	user.JID = strings.Replace(user.Conn.Info.Wid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, 1)
	user.addToJIDMap()
	user.SetSession(&session)
	_ = c.WriteJSON(map[string]interface{}{
		"success": true,
		"jid":     user.JID,
	})
	user.PostLogin()
}
