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
	"fmt"
	"net/http"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"
)

const (
	WALoggedOut        bridge.StateErrorCode = "wa-logged-out"
	WAMainDeviceGone   bridge.StateErrorCode = "wa-main-device-gone"
	WAUnknownLogout    bridge.StateErrorCode = "wa-unknown-logout"
	WANotConnected     bridge.StateErrorCode = "wa-not-connected"
	WAConnecting       bridge.StateErrorCode = "wa-connecting"
	WAKeepaliveTimeout bridge.StateErrorCode = "wa-keepalive-timeout"
	WAPhoneOffline     bridge.StateErrorCode = "wa-phone-offline"
	WAConnectionFailed bridge.StateErrorCode = "wa-connection-failed"
)

func init() {
	bridge.StateHumanErrors.Update(bridge.StateErrorMap{
		WALoggedOut:        "You were logged out from another device. Relogin to continue using the bridge.",
		WAMainDeviceGone:   "Your phone was logged out from WhatsApp. Relogin to continue using the bridge.",
		WAUnknownLogout:    "You were logged out for an unknown reason. Relogin to continue using the bridge.",
		WANotConnected:     "You're not connected to WhatsApp",
		WAConnecting:       "Reconnecting to WhatsApp...",
		WAKeepaliveTimeout: "The WhatsApp web servers are not responding. The bridge will try to reconnect.",
		WAPhoneOffline:     "Your phone hasn't been seen in over 12 days. The bridge is currently connected, but will get disconnected if you don't open the app soon.",
		WAConnectionFailed: "Connecting to the WhatsApp web servers failed.",
	})
}

func (user *User) GetRemoteID() string {
	if user == nil || user.JID.IsEmpty() {
		return ""
	}
	return fmt.Sprintf("%s_a%d_d%d", user.JID.User, user.JID.Agent, user.JID.Device)
}

func (user *User) GetRemoteName() string {
	if user == nil || user.JID.IsEmpty() {
		return ""
	}
	return fmt.Sprintf("+%s", user.JID.User)
}

func (prov *ProvisioningAPI) BridgeStatePing(w http.ResponseWriter, r *http.Request) {
	if !prov.bridge.AS.CheckServerToken(w, r) {
		return
	}
	userID := r.URL.Query().Get("user_id")
	user := prov.bridge.GetUserByMXID(id.UserID(userID))
	var global bridge.State
	global.StateEvent = bridge.StateRunning
	var remote bridge.State
	if user.IsConnected() {
		if user.Client.IsLoggedIn() {
			remote.StateEvent = bridge.StateConnected
		} else if user.Session != nil {
			remote.StateEvent = bridge.StateConnecting
			remote.Error = WAConnecting
		} // else: unconfigured
	} else if user.Session != nil {
		remote.StateEvent = bridge.StateBadCredentials
		remote.Error = WANotConnected
	} // else: unconfigured
	global = global.Fill(nil)
	resp := bridge.GlobalState{
		BridgeState:  global,
		RemoteStates: map[string]bridge.State{},
	}
	if len(remote.StateEvent) > 0 {
		remote = remote.Fill(user)
		resp.RemoteStates[remote.RemoteID] = remote
	}
	user.log.Debugfln("Responding bridge state in bridge status endpoint: %+v", resp)
	jsonResponse(w, http.StatusOK, &resp)
	if len(resp.RemoteStates) > 0 {
		user.BridgeState.SetPrev(remote)
	}
}
