// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"

	"go.mau.fi/whatsmeow"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"
)

type ProvisioningAPI struct {
	bridge *WABridge
	log    log.Logger
}

func (prov *ProvisioningAPI) Init() {
	prov.log = prov.bridge.Log.Sub("Provisioning")

	prov.log.Debugln("Enabling provisioning API at", prov.bridge.Config.Bridge.Provisioning.Prefix)
	r := prov.bridge.AS.Router.PathPrefix(prov.bridge.Config.Bridge.Provisioning.Prefix).Subrouter()
	r.Use(prov.AuthMiddleware)
	r.HandleFunc("/v1/ping", prov.Ping).Methods(http.MethodGet)
	r.HandleFunc("/v1/login", prov.Login).Methods(http.MethodGet)
	r.HandleFunc("/v1/logout", prov.Logout).Methods(http.MethodPost)
	r.HandleFunc("/v1/delete_session", prov.DeleteSession).Methods(http.MethodPost)
	r.HandleFunc("/v1/disconnect", prov.Disconnect).Methods(http.MethodPost)
	r.HandleFunc("/v1/reconnect", prov.Reconnect).Methods(http.MethodPost)
	r.HandleFunc("/v1/debug/appstate/{name}", prov.SyncAppState).Methods(http.MethodPost)
	r.HandleFunc("/v1/debug/retry", prov.SendRetryReceipt).Methods(http.MethodPost)
	r.HandleFunc("/v1/contacts", prov.ListContacts).Methods(http.MethodGet)
	r.HandleFunc("/v1/groups", prov.ListGroups).Methods(http.MethodGet)
	r.HandleFunc("/v1/resolve_identifier/{number}", prov.ResolveIdentifier).Methods(http.MethodGet)
	r.HandleFunc("/v1/bulk_resolve_identifier", prov.BulkResolveIdentifier).Methods(http.MethodPost)
	r.HandleFunc("/v1/pm/{number}", prov.StartPM).Methods(http.MethodPost)
	r.HandleFunc("/v1/bridge/{groupID}/{roomIDorAlias}", prov.BridgeGroup).Methods(http.MethodPost)
	r.HandleFunc("/v1/open/{groupID}", prov.OpenGroup).Methods(http.MethodPost)
	r.HandleFunc("/v1/create/{roomIDorAlias}", prov.CreateGroup).Methods(http.MethodPost)
	r.HandleFunc("/v1/unbridge/{roomIDorAlias}", prov.UnbridgePortal).Methods(http.MethodPost)
	r.HandleFunc("/v1/delete/{roomIDorAlias}", prov.DeletePortal).Methods(http.MethodPost)
	r.HandleFunc("/v1/set_relay/{roomIDorAlias}", prov.SetRelay).Methods(http.MethodPost)
	r.HandleFunc("/v1/unset_relay/{roomIDorAlias}", prov.UnsetRelay).Methods(http.MethodPost)
	r.HandleFunc("/v1/invite_link/{roomIDorAlias}", prov.GetInvite).Methods(http.MethodGet)
	prov.bridge.AS.Router.HandleFunc("/_matrix/app/com.beeper.asmux/ping", prov.BridgeStatePing).Methods(http.MethodPost)
	prov.bridge.AS.Router.HandleFunc("/_matrix/app/com.beeper.bridge_state", prov.BridgeStatePing).Methods(http.MethodPost)

	// Deprecated, just use /disconnect
	r.HandleFunc("/v1/delete_connection", prov.Disconnect).Methods(http.MethodPost)
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
		if auth != prov.bridge.Config.Bridge.Provisioning.SharedSecret {
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
	user.removeFromJIDMap(bridge.State{StateEvent: bridge.StateLoggedOut})
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
	user.BridgeState.Send(bridge.State{StateEvent: bridge.StateBadCredentials, Error: WANotConnected})
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
		user.BridgeState.Send(bridge.State{StateEvent: bridge.StateTransientDisconnect, Error: WANotConnected})
		user.Connect()
		jsonResponse(w, http.StatusAccepted, Response{true, "Restarted connection to WhatsApp"})
	}
}

type debugRetryReceiptContent struct {
	ID          types.MessageID `json:"id"`
	From        types.JID       `json:"from"`
	Recipient   types.JID       `json:"recipient"`
	Participant types.JID       `json:"participant"`
	Timestamp   int64           `json:"timestamp"`
	Count       int             `json:"count"`

	ForceIncludeIdentity bool `json:"force_include_identity"`
}

func (prov *ProvisioningAPI) SendRetryReceipt(w http.ResponseWriter, r *http.Request) {
	var req debugRetryReceiptContent
	user := r.Context().Value("user").(*User)
	if user == nil || user.Client == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "User is not connected to WhatsApp",
			ErrCode: "no session",
		})
		return
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Failed to parse request JSON",
			ErrCode: "bad json",
		})
	} else {
		node := &waBinary.Node{
			Attrs: waBinary.Attrs{
				"id":   string(req.ID),
				"from": req.From,
				"t":    strconv.FormatInt(req.Timestamp, 10),
			},
		}
		if !req.Recipient.IsEmpty() {
			node.Attrs["recipient"] = req.Recipient
		}
		if !req.Participant.IsEmpty() {
			node.Attrs["participant"] = req.Participant
		}
		if req.Count > 0 {
			node.Content = []waBinary.Node{{
				Tag:   "enc",
				Attrs: waBinary.Attrs{"count": strconv.Itoa(req.Count)},
			}}
		}
		user.Client.DangerousInternals().SendRetryReceipt(node, req.ForceIncludeIdentity)
	}
}

func (prov *ProvisioningAPI) SyncAppState(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	if user == nil || user.Client == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "User is not connected to WhatsApp",
			ErrCode: "no session",
		})
		return
	}

	vars := mux.Vars(r)
	nameStr := vars["name"]
	if len(nameStr) == 0 {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "The `name` parameter is required",
			ErrCode: "missing-name-param",
		})
		return
	}
	var name appstate.WAPatchName
	for _, existingName := range appstate.AllPatchNames {
		if nameStr == string(existingName) {
			name = existingName
		}
	}
	if len(name) == 0 {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   fmt.Sprintf("'%s' is not a valid app state patch name", nameStr),
			ErrCode: "invalid-name-param",
		})
		return
	}
	fullStr := r.URL.Query().Get("full")
	fullSync := len(fullStr) > 0 && (fullStr == "1" || strings.ToLower(fullStr)[0] == 't')
	err := user.Client.FetchAppState(name, fullSync, false)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{false, err.Error(), "sync-fail"})
	} else {
		jsonResponse(w, http.StatusOK, Response{true, fmt.Sprintf("Synced app state %s", name)})
	}
}

func (prov *ProvisioningAPI) ListContacts(w http.ResponseWriter, r *http.Request) {
	if user := r.Context().Value("user").(*User); user.Session == nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if contacts, err := user.Session.Contacts.GetAllContacts(); err != nil {
		prov.log.Errorfln("Failed to fetch %s's contacts: %v", user.MXID, err)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal server error while fetching contact list",
			ErrCode: "failed to get contacts",
		})
	} else {
		augmentedContacts := map[types.JID]interface{}{}
		for jid, contact := range contacts {
			var avatarUrl id.ContentURI
			if puppet := prov.bridge.DB.Puppet.Get(jid); puppet != nil {
				avatarUrl = puppet.AvatarURL
			}
			augmentedContacts[jid] = map[string]interface{}{
				"Found":        contact.Found,
				"FirstName":    contact.FirstName,
				"FullName":     contact.FullName,
				"PushName":     contact.PushName,
				"BusinessName": contact.BusinessName,
				"AvatarURL":    avatarUrl,
			}
		}
		jsonResponse(w, http.StatusOK, augmentedContacts)
	}
}

func (prov *ProvisioningAPI) ListGroups(w http.ResponseWriter, r *http.Request) {
	if user := r.Context().Value("user").(*User); user.Session == nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if groups, err := user.getCachedGroupList(); err != nil {
		prov.log.Errorfln("Failed to fetch %s's groups: %v", user.MXID, err)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal server error while fetching group list",
			ErrCode: "failed to get groups",
		})
	} else {
		jsonResponse(w, http.StatusOK, groups)
	}
}

type OtherUserInfo struct {
	MXID   id.UserID     `json:"mxid"`
	JID    types.JID     `json:"jid"`
	Name   string        `json:"displayname"`
	Avatar id.ContentURI `json:"avatar_url"`
}

type PermsInfo struct {
	BotLevel     int                     `json:"bot_level"`
	UserLevel    int                     `json:"user_level"`
	MissingPerms *map[string]interface{} `json:"missing_permissions"`
}

type PortalInfo struct {
	RoomID      id.RoomID        `json:"room_id"`
	OtherUser   *OtherUserInfo   `json:"other_user,omitempty"`
	GroupInfo   *types.GroupInfo `json:"group_info,omitempty"`
	JustCreated bool             `json:"just_created"`
	PermsInfo   *PermsInfo       `json:"permissions_info,omitempty"`
}

type LinkInfo struct {
	InviteLink string `json:"invite_link"`
}

func looksEmaily(str string) bool {
	for _, char := range str {
		// Characters that are usually in emails, but shouldn't be in phone numbers
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '@' {
			return true
		}
	}
	return false
}

func (prov *ProvisioningAPI) resolveIdentifier(w http.ResponseWriter, r *http.Request) (types.JID, *User) {
	number, _ := mux.Vars(r)["number"]
	if strings.HasSuffix(number, "@"+types.DefaultUserServer) {
		jid, _ := types.ParseJID(number)
		number = "+" + jid.User
	}
	if looksEmaily(number) {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "WhatsApp only supports phone numbers as user identifiers",
			ErrCode: "number looks like email",
		})
	} else if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if resp, err := user.Client.IsOnWhatsApp([]string{number}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   fmt.Sprintf("Failed to check if number is on WhatsApp: %v", err),
			ErrCode: "error checking number",
		})
	} else if len(resp) == 0 {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Didn't get a response to checking if the number is on WhatsApp",
			ErrCode: "error checking number",
		})
	} else if !resp[0].IsIn {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   fmt.Sprintf("The server said +%s is not on WhatsApp", resp[0].JID.User),
			ErrCode: "not on whatsapp",
		})
	} else {
		return resp[0].JID, user
	}
	return types.EmptyJID, nil
}

func (prov *ProvisioningAPI) StartPM(w http.ResponseWriter, r *http.Request) {
	jid, user := prov.resolveIdentifier(w, r)
	if jid.IsEmpty() || user == nil {
		// resolveIdentifier already responded with an error
		return
	}
	portal, puppet, justCreated, err := user.StartPM(jid, "provisioning API PM")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error: fmt.Sprintf("Failed to create portal: %v", err),
		})
	}
	status := http.StatusOK
	if justCreated {
		status = http.StatusCreated
	}
	jsonResponse(w, status, PortalInfo{
		RoomID: portal.MXID,
		OtherUser: &OtherUserInfo{
			JID:    puppet.JID,
			MXID:   puppet.MXID,
			Name:   puppet.Displayname,
			Avatar: puppet.AvatarURL,
		},
		JustCreated: justCreated,
	})
}

func (prov *ProvisioningAPI) ResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	jid, user := prov.resolveIdentifier(w, r)
	if jid.IsEmpty() || user == nil {
		// resolveIdentifier already responded with an error
		return
	}
	portal := user.GetPortalByJID(jid)
	puppet := user.bridge.GetPuppetByJID(jid)
	jsonResponse(w, http.StatusOK, PortalInfo{
		RoomID: portal.MXID,
		OtherUser: &OtherUserInfo{
			JID:    puppet.JID,
			MXID:   puppet.MXID,
			Name:   puppet.Displayname,
			Avatar: puppet.AvatarURL,
		},
	})
}

type ReqBulkResolveIdentifier struct {
	Numbers []string `json:"numbers"`
}

func (prov *ProvisioningAPI) BulkResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	var req ReqBulkResolveIdentifier
	var resp []types.IsOnWhatsAppResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Failed to parse request JSON",
			ErrCode: "bad json",
		})
	} else if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if resp, err = user.Client.IsOnWhatsApp(req.Numbers); err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   fmt.Sprintf("Failed to check if number is on WhatsApp: %v", err),
			ErrCode: "error checking number",
		})
	} else {
		jsonResponse(w, http.StatusOK, resp)
	}
}

func (prov *ProvisioningAPI) BridgeGroup(w http.ResponseWriter, r *http.Request) {
	groupID := mux.Vars(r)["groupID"]
	roomArg := mux.Vars(r)["roomIDorAlias"]
	if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if roomID, isAlias, err := prov.bridge.ResolveRoomArg(roomArg); err != nil || roomID == "" {
		if isAlias {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "Failed to resolve room alias",
				ErrCode: "error resolving room alias",
			})
		} else {
			jsonResponse(w, http.StatusBadRequest, Error{
				Error:   "Invalid room ID",
				ErrCode: "invalid room id",
			})
		}
	} else if err := prov.bridge.Bot.EnsureJoined(roomID); err != nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Bridge bot is not in target room and cannot join it",
			ErrCode: "room unknown",
		})
	} else if portal := prov.bridge.GetPortalByMXID(roomID); portal != nil {
		if portal.MXID != roomID {
			jsonResponse(w, http.StatusConflict, Error{
				Error:   "Room is already a WhatsApp portal room",
				ErrCode: "room already bridged",
			})
		} else {
			jsonResponse(w, http.StatusOK, Error{
				Error:   "WhatsApp group is already bridged to that Matrix room",
				ErrCode: "bridge exists",
			})
		}
	} else if !userHasPowerLevel(roomID, prov.bridge.AS.BotIntent(), user, "bridge") {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "User does not have the permissions to bridge that room",
			ErrCode: "not enough permissions",
		})
	} else if pInfo := GetMissingRequiredPerms(roomID, prov.bridge.AS.BotIntent(), prov.bridge, prov.log); pInfo != nil {
		missingRequiredPermsResponse(w, pInfo)
	} else if jid, err := types.ParseJID(groupID); err != nil || jid.Server != types.GroupServer || (!strings.ContainsRune(jid.User, '-') && len(jid.User) < 15) {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Invalid group ID",
			ErrCode: "invalid group id",
		})
	} else if info, err := user.Client.GetGroupInfo(jid); err != nil {
		// TODO return better responses for different errors (like ErrGroupNotFound and ErrNotInGroup)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   fmt.Sprintf("Failed to get group info: %v", err),
			ErrCode: "error getting group info",
		})
	} else {
		prov.log.Debugln("Importing", jid, "for", user.MXID)
		portal := user.GetPortalByJID(info.JID)
		if portal.MXID == roomID {
			jsonResponse(w, http.StatusOK, Error{
				Error:   "WhatsApp group is already bridged to that Matrix room",
				ErrCode: "bridge exists",
			})
			return
		} else if len(portal.MXID) > 0 {
			if !userHasPowerLevel(portal.MXID, prov.bridge.AS.BotIntent(), user, "unbridge") {
				jsonResponse(w, http.StatusForbidden, Error{
					Error:   "User does not have the permissions to unbridge that room",
					ErrCode: "not enough permissions",
				})
				return
			}
			switch strings.ToLower(r.URL.Query().Get("force")) {
			case "delete":
				portal.Cleanup("Portal deleted (moving to another room)", false)
			case "unbridge":
				portal.Cleanup("Room unbridged (portal moving to another room)", true)
			default:
				jsonResponse(w, http.StatusConflict, Error{
					Error:   "WhatsApp group is already bridged to another Matrix room",
					ErrCode: "group already bridged",
				})
				return
			}
		}
		portal.roomCreateLock.Lock()
		defer portal.roomCreateLock.Unlock()
		portal.BridgeMatrixRoom(roomID, user, info)
		jsonResponse(w, http.StatusOK, PortalInfo{
			RoomID:    portal.MXID,
			GroupInfo: info,
			PermsInfo: getPermsInfo(GetMissingOptionalPerms(roomID, prov.bridge.AS.BotIntent(), prov.bridge, prov.log)),
		})
	}
}

func (prov *ProvisioningAPI) OpenGroup(w http.ResponseWriter, r *http.Request) {
	groupID, _ := mux.Vars(r)["groupID"]
	if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if jid, err := types.ParseJID(groupID); err != nil || jid.Server != types.GroupServer || (!strings.ContainsRune(jid.User, '-') && len(jid.User) < 15) {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Invalid group ID",
			ErrCode: "invalid group id",
		})
	} else if info, err := user.Client.GetGroupInfo(jid); err != nil {
		// TODO return better responses for different errors (like ErrGroupNotFound and ErrNotInGroup)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   fmt.Sprintf("Failed to get group info: %v", err),
			ErrCode: "error getting group info",
		})
	} else {
		prov.log.Debugln("Importing", jid, "for", user.MXID)
		portal := user.GetPortalByJID(info.JID)
		status := http.StatusOK
		if len(portal.MXID) == 0 {
			err = portal.CreateMatrixRoom(user, info, true, true)
			if err != nil {
				jsonResponse(w, http.StatusInternalServerError, Error{
					Error: fmt.Sprintf("Failed to create portal: %v", err),
				})
				return
			}
			status = http.StatusCreated
		}
		jsonResponse(w, status, PortalInfo{
			RoomID:      portal.MXID,
			GroupInfo:   info,
			JustCreated: status == http.StatusCreated,
		})
	}
}

func (prov *ProvisioningAPI) CreateGroup(w http.ResponseWriter, r *http.Request) {
	roomArg := mux.Vars(r)["roomIDorAlias"]
	if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if roomID, isAlias, err := prov.bridge.ResolveRoomArg(roomArg); err != nil || roomID == "" {
		if isAlias {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "Failed to resolve room alias",
				ErrCode: "error resolving room alias",
			})
		} else {
			jsonResponse(w, http.StatusBadRequest, Error{
				Error:   "Invalid room ID",
				ErrCode: "invalid room id",
			})
		}
	} else if err := prov.bridge.Bot.EnsureJoined(roomID); err != nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Bridge bot is not in target room and cannot join it",
			ErrCode: "room unknown",
		})
	} else if portal := prov.bridge.GetPortalByMXID(roomID); portal != nil {
		if portal.MXID != roomID {
			jsonResponse(w, http.StatusConflict, Error{
				Error:   "Room is already a WhatsApp portal room",
				ErrCode: "room already bridged",
			})
		} else {
			jsonResponse(w, http.StatusOK, Error{
				Error:   "WhatsApp group is already bridged to that Matrix room",
				ErrCode: "bridge exists",
			})
		}
	} else if !userHasPowerLevel(roomID, prov.bridge.AS.BotIntent(), user, "bridge") {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "User does not have the permissions to bridge that room",
			ErrCode: "not enough permissions",
		})
	} else if pInfo := GetMissingRequiredPerms(roomID, prov.bridge.AS.BotIntent(), prov.bridge, prov.log); pInfo != nil {
		missingRequiredPermsResponse(w, pInfo)
	} else if portal, info, errMsg := createGroup(user, roomID, &prov.log, nil); errMsg != "" {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   errMsg,
			ErrCode: "error creating group",
		})
	} else {
		jsonResponse(w, http.StatusOK, PortalInfo{
			RoomID:    portal.MXID,
			GroupInfo: info,
			PermsInfo: getPermsInfo(GetMissingOptionalPerms(roomID, prov.bridge.AS.BotIntent(), prov.bridge, prov.log)),
		})
	}
}

func (prov *ProvisioningAPI) UnbridgePortal(w http.ResponseWriter, r *http.Request) {
	prov.CleanupPortal(w, r, false)
}

func (prov *ProvisioningAPI) DeletePortal(w http.ResponseWriter, r *http.Request) {
	prov.CleanupPortal(w, r, true)
}

func (prov *ProvisioningAPI) CleanupPortal(w http.ResponseWriter, r *http.Request, deletePortal bool) {
	roomArg := mux.Vars(r)["roomIDorAlias"]
	user := r.Context().Value("user").(*User)
	if roomID, isAlias, err := prov.bridge.ResolveRoomArg(roomArg); err != nil || roomID == "" {
		if isAlias {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "Failed to resolve room alias",
				ErrCode: "error resolving room alias",
			})
		} else {
			jsonResponse(w, http.StatusBadRequest, Error{
				Error:   "Invalid room ID",
				ErrCode: "invalid room id",
			})
		}
	} else if portal := prov.bridge.GetPortalByMXID(roomID); portal == nil {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "Room is not bridged to WhatsApp",
			ErrCode: "room not bridged",
		})
	} else if deletePortal && !user.Admin && canDeletePortal(portal, user.MXID) {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "Only bridge admins can delete portals with other Matrix users",
			ErrCode: "not admin",
		})
	} else if !checkUnbridgePermission(portal, user) {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "User does not have the permissions to unbridge that room",
			ErrCode: "not enough permissions",
		})
	} else {
		var requestAction, responseAction string
		if deletePortal {
			requestAction = "deletion"
			responseAction = "Portal deleted"
		} else {
			requestAction = "unbridging"
			responseAction = "Room unbridged"
		}
		portal.log.Infoln("%s requested %s of portal.", user.MXID, requestAction)
		portal.Delete()
		portal.Cleanup(responseAction, !deletePortal)
		jsonResponse(w, http.StatusOK, Response{true, responseAction})
	}
}

func (prov *ProvisioningAPI) SetRelay(w http.ResponseWriter, r *http.Request) {
	prov.EditRelay(w, r, true)
}

func (prov *ProvisioningAPI) UnsetRelay(w http.ResponseWriter, r *http.Request) {
	prov.EditRelay(w, r, false)
}

func (prov *ProvisioningAPI) EditRelay(w http.ResponseWriter, r *http.Request, set bool) {
	roomArg := mux.Vars(r)["roomIDorAlias"]
	if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if roomID, isAlias, err := prov.bridge.ResolveRoomArg(roomArg); err != nil || roomID == "" {
		if isAlias {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "Failed to resolve room alias",
				ErrCode: "error resolving room alias",
			})
		} else {
			jsonResponse(w, http.StatusBadRequest, Error{
				Error:   "Invalid room ID",
				ErrCode: "invalid room id",
			})
		}
	} else if portal := prov.bridge.GetPortalByMXID(roomID); portal == nil {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "Room is not bridged to WhatsApp",
			ErrCode: "room not bridged",
		})
	} else if !prov.bridge.Config.Bridge.Relay.Enabled {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "Relay mode is not enabled on this instance of the bridge",
			ErrCode: "relay mode not enabled",
		})
	} else if prov.bridge.Config.Bridge.Relay.AdminOnly && !user.Admin {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "Only admins are allowed to enable relay mode on this instance of the bridge",
			ErrCode: "relay mode not allowed for non-admins",
		})
	} else {
		var action string
		if set {
			portal.RelayUserID = user.MXID
			action = "set"
		} else {
			portal.RelayUserID = ""
			action = "unset"
		}
		portal.Update(nil)
		jsonResponse(w, http.StatusOK, Response{true, "Relay user " + action})
	}
}

func (prov *ProvisioningAPI) GetInvite(w http.ResponseWriter, r *http.Request) {
	roomArg := mux.Vars(r)["roomIDorAlias"]
	reset := r.URL.Query().Has("reset")
	if user := r.Context().Value("user").(*User); !user.IsLoggedIn() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged into WhatsApp",
			ErrCode: "no session",
		})
	} else if roomID, isAlias, err := prov.bridge.ResolveRoomArg(roomArg); err != nil || roomID == "" {
		if isAlias {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "Failed to resolve room alias",
				ErrCode: "error resolving room alias",
			})
		} else {
			jsonResponse(w, http.StatusBadRequest, Error{
				Error:   "Invalid room ID",
				ErrCode: "invalid room id",
			})
		}
	} else if portal := prov.bridge.GetPortalByMXID(roomID); portal == nil {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "Room is not bridged to WhatsApp",
			ErrCode: "room not bridged",
		})
	} else if portal.IsPrivateChat() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Can't get invite link to private chat",
			ErrCode: "room not bridged to group",
		})
	} else if portal.IsBroadcastList() {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Can't get invite link to broadcast list",
			ErrCode: "room not bridged to group",
		})
	} else if link, err := user.Client.GetGroupInviteLink(portal.Key.JID, reset); err != nil {
		prov.log.Errorln("Failed to get invite link: %v", err)
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Failed to get invite link",
			ErrCode: "failed to get invite link",
		})
	} else {
		jsonResponse(w, http.StatusOK, LinkInfo{link})
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
		if user.Session != nil {
			wa["platform"] = user.Session.Platform
		}
	}
	if user.Client != nil {
		wa["conn"] = map[string]interface{}{
			"is_connected": user.Client.IsConnected(),
			"is_logged_in": user.Client.IsLoggedIn(),
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

func missingRequiredPermsResponse(w http.ResponseWriter, info *MissingPermsInfo) {
	jsonResponse(w, http.StatusForbidden, map[string]interface{}{
		"error":            "Bridge is missing required permissions in this room",
		"errcode":          "bridge missing permissions",
		"permissions_info": getPermsInfo(info),
	})
}

func getPermsInfo(info *MissingPermsInfo) *PermsInfo {
	if info == nil {
		return nil
	}

	var obj PermsInfo
	obj.BotLevel = info.BotLevel
	obj.UserLevel = info.UserLevel

	missingPerms := make(map[string]interface{})
	obj.MissingPerms = &missingPerms

	const eventsKey = "events"
	eventPerms := make(map[string]int)
	missingPerms[eventsKey] = eventPerms

	for _, perm := range info.MissingPerms {
		if perm.IsEvent {
			eventPerms[perm.Key] = perm.ReqLevel
		} else {
			missingPerms[perm.Key] = perm.ReqLevel
		}
	}
	if len(eventPerms) == 0 {
		delete(missingPerms, eventsKey)
	}

	return &obj
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
	user.removeFromJIDMap(bridge.State{StateEvent: bridge.StateLoggedOut})
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

	if userTimezone := r.URL.Query().Get("tz"); userTimezone != "" {
		user.Timezone = userTimezone
		user.Update()
	}

	qrChan, err := user.Login(ctx)
	if err != nil {
		user.log.Errorln("Failed to log in from provisioning API:", err)
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
	Segment.Track(user.MXID, "$login_start")

	for {
		select {
		case evt := <-qrChan:
			switch evt.Event {
			case whatsmeow.QRChannelSuccess.Event:
				jid := user.Client.Store.ID
				user.log.Debugln("Successful login as", jid, "via provisioning API")
				Segment.Track(user.MXID, "$login_success")
				_ = c.WriteJSON(map[string]interface{}{
					"success":  true,
					"jid":      jid,
					"phone":    fmt.Sprintf("+%s", jid.User),
					"platform": user.Client.Store.Platform,
				})
			case whatsmeow.QRChannelTimeout.Event:
				user.log.Debugln("Login via provisioning API timed out")
				errCode := "login timed out"
				Segment.Track(user.MXID, "$login_failure", map[string]interface{}{"error": errCode})
				_ = c.WriteJSON(Error{
					Error:   "QR code scan timed out. Please try again.",
					ErrCode: errCode,
				})
			case whatsmeow.QRChannelErrUnexpectedEvent.Event:
				user.log.Debugln("Login via provisioning API failed due to unexpected event")
				errCode := "unexpected event"
				Segment.Track(user.MXID, "$login_failure", map[string]interface{}{"error": errCode})
				_ = c.WriteJSON(Error{
					Error:   "Got unexpected event while waiting for QRs, perhaps you're already logged in?",
					ErrCode: errCode,
				})
			case whatsmeow.QRChannelClientOutdated.Event:
				user.log.Debugln("Login via provisioning API failed due to outdated client")
				errCode := "bridge outdated"
				Segment.Track(user.MXID, "$login_failure", map[string]interface{}{"error": errCode})
				_ = c.WriteJSON(Error{
					Error:   "Got client outdated error while waiting for QRs. The bridge must be updated to continue.",
					ErrCode: errCode,
				})
			case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
				errCode := "multidevice not enabled"
				Segment.Track(user.MXID, "$login_failure", map[string]interface{}{"error": errCode})
				_ = c.WriteJSON(Error{
					Error:   "Please enable the WhatsApp multidevice beta and scan the QR code again.",
					ErrCode: errCode,
				})
				continue
			case "error":
				errCode := "fatal error"
				Segment.Track(user.MXID, "$login_failure", map[string]interface{}{"error": errCode})
				_ = c.WriteJSON(Error{
					Error:   "Fatal error while logging in",
					ErrCode: errCode,
				})
			case "code":
				Segment.Track(user.MXID, "$qrcode_retrieved")
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
