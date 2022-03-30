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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
)

type BridgeStateEvent string

const (
	StateUnconfigured        BridgeStateEvent = "UNCONFIGURED"
	StateRunning             BridgeStateEvent = "RUNNING"
	StateConnecting          BridgeStateEvent = "CONNECTING"
	StateBackfilling         BridgeStateEvent = "BACKFILLING"
	StateConnected           BridgeStateEvent = "CONNECTED"
	StateTransientDisconnect BridgeStateEvent = "TRANSIENT_DISCONNECT"
	StateBadCredentials      BridgeStateEvent = "BAD_CREDENTIALS"
	StateUnknownError        BridgeStateEvent = "UNKNOWN_ERROR"
	StateLoggedOut           BridgeStateEvent = "LOGGED_OUT"
)

type BridgeErrorCode string

const (
	WALoggedOut     BridgeErrorCode = "wa-logged-out"
	WAAccountBanned BridgeErrorCode = "wa-account-banned"
	WAUnknownLogout BridgeErrorCode = "wa-unknown-logout"
	WANotConnected  BridgeErrorCode = "wa-not-connected"
	WAConnecting    BridgeErrorCode = "wa-connecting"
	WAServerTimeout BridgeErrorCode = "wa-server-timeout"
	WAPhoneOffline  BridgeErrorCode = "wa-phone-offline"
)

var bridgeHumanErrors = map[BridgeErrorCode]string{
	WALoggedOut:     "You were logged out from another device. Relogin to continue using the bridge.",
	WAAccountBanned: "Your account was banned from WhatsApp. You can contact support from the WhatsApp mobile app on your phone.",
	WAUnknownLogout: "You were logged out for an unknown reason. Relogin to continue using the bridge.",
	WANotConnected:  "You're not connected to WhatsApp",
	WAConnecting:    "Reconnecting to WhatsApp...",
	WAServerTimeout: "The WhatsApp web servers are not responding. The bridge will try to reconnect.",
	WAPhoneOffline:  "Your phone hasn't been seen in over 12 days. The bridge is currently connected, but will get disconnected if you don't open the app soon.",
}

type BridgeState struct {
	StateEvent BridgeStateEvent `json:"state_event"`
	Timestamp  int64            `json:"timestamp"`
	TTL        int              `json:"ttl"`

	Source  string          `json:"source,omitempty"`
	Error   BridgeErrorCode `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`

	UserID     id.UserID `json:"user_id,omitempty"`
	RemoteID   string    `json:"remote_id,omitempty"`
	RemoteName string    `json:"remote_name,omitempty"`
}

type GlobalBridgeState struct {
	RemoteStates map[string]BridgeState `json:"remoteState"`
	BridgeState  BridgeState            `json:"bridgeState"`
}

func (pong BridgeState) fill(user *User) BridgeState {
	if user != nil {
		pong.UserID = user.MXID
		pong.RemoteID = fmt.Sprintf("%s_a%d_d%d", user.JID.User, user.JID.Agent, user.JID.Device)
		pong.RemoteName = fmt.Sprintf("+%s", user.JID.User)
	}

	pong.Timestamp = time.Now().Unix()
	pong.Source = "bridge"
	if len(pong.Error) > 0 {
		pong.TTL = 60
		pong.Message = bridgeHumanErrors[pong.Error]
	} else {
		pong.TTL = 240
	}
	return pong
}

func (pong *BridgeState) shouldDeduplicate(newPong *BridgeState) bool {
	if pong == nil || pong.StateEvent != newPong.StateEvent || pong.Error != newPong.Error {
		return false
	}
	return pong.Timestamp+int64(pong.TTL/5) > time.Now().Unix()
}

func (bridge *Bridge) createBridgeStateRequest(ctx context.Context, state *BridgeState) (req *http.Request, err error) {
	var body bytes.Buffer
	if err = json.NewEncoder(&body).Encode(&state); err != nil {
		return nil, fmt.Errorf("failed to encode bridge state JSON: %w", err)
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodPost, bridge.Config.Homeserver.StatusEndpoint, &body)
	if err != nil {
		return
	}

	req.Header.Set("Authorization", "Bearer "+bridge.Config.AppService.ASToken)
	req.Header.Set("Content-Type", "application/json")
	return
}

func sendPreparedBridgeStateRequest(logger log.Logger, req *http.Request) bool {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Warnln("Failed to send bridge state update:", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(resp.Body)
		if respBody != nil {
			respBody = bytes.ReplaceAll(respBody, []byte("\n"), []byte("\\n"))
		}
		logger.Warnfln("Unexpected status code %d sending bridge state update: %s", resp.StatusCode, respBody)
		return false
	}
	return true
}

func (bridge *Bridge) sendGlobalBridgeState(state BridgeState) {
	if len(bridge.Config.Homeserver.StatusEndpoint) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if req, err := bridge.createBridgeStateRequest(ctx, &state); err != nil {
		bridge.Log.Warnln("Failed to prepare global bridge state update request:", err)
	} else if ok := sendPreparedBridgeStateRequest(bridge.Log, req); ok {
		bridge.Log.Debugfln("Sent new global bridge state %+v", state)
	}
}

func (user *User) sendBridgeState(state BridgeState) {
	if len(user.bridge.Config.Homeserver.StatusEndpoint) == 0 {
		return
	}

	state = state.fill(user)
	if user.prevBridgeStatus != nil && user.prevBridgeStatus.shouldDeduplicate(&state) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if req, err := user.bridge.createBridgeStateRequest(ctx, &state); err != nil {
		user.log.Warnln("Failed to prepare bridge state update request:", err)
	} else if ok := sendPreparedBridgeStateRequest(user.log, req); ok {
		user.prevBridgeStatus = &state
		user.log.Debugfln("Sent new bridge state %+v", state)
	}
}

func (user *User) GetPrevBridgeState() BridgeState {
	if user.prevBridgeStatus != nil {
		return *user.prevBridgeStatus
	}
	return BridgeState{}
}

func (prov *ProvisioningAPI) BridgeStatePing(w http.ResponseWriter, r *http.Request) {
	if !prov.bridge.AS.CheckServerToken(w, r) {
		return
	}
	userID := r.URL.Query().Get("user_id")
	user := prov.bridge.GetUserByMXID(id.UserID(userID))
	var global BridgeState
	global.StateEvent = StateRunning
	var remote BridgeState
	if user.IsConnected() {
		if user.Client.IsLoggedIn() {
			remote.StateEvent = StateConnected
		} else if user.Session != nil {
			remote.StateEvent = StateConnecting
			remote.Error = WAConnecting
		} // else: unconfigured
	} else if user.Session != nil {
		remote.StateEvent = StateBadCredentials
		remote.Error = WANotConnected
	} // else: unconfigured
	global = global.fill(nil)
	resp := GlobalBridgeState{
		BridgeState:  global,
		RemoteStates: map[string]BridgeState{},
	}
	if len(remote.StateEvent) > 0 {
		remote = remote.fill(user)
		resp.RemoteStates[remote.RemoteID] = remote
	}
	user.log.Debugfln("Responding bridge state in bridge status endpoint: %+v", resp)
	jsonResponse(w, http.StatusOK, &resp)
	if len(resp.RemoteStates) > 0 {
		user.prevBridgeStatus = &remote
	}
}
