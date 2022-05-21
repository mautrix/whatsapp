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
	"runtime/debug"
	"time"

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
	WALoggedOut        BridgeErrorCode = "wa-logged-out"
	WAMainDeviceGone   BridgeErrorCode = "wa-main-device-gone"
	WAUnknownLogout    BridgeErrorCode = "wa-unknown-logout"
	WANotConnected     BridgeErrorCode = "wa-not-connected"
	WAConnecting       BridgeErrorCode = "wa-connecting"
	WAKeepaliveTimeout BridgeErrorCode = "wa-keepalive-timeout"
	WAPhoneOffline     BridgeErrorCode = "wa-phone-offline"
	WAConnectionFailed BridgeErrorCode = "wa-connection-failed"
)

var bridgeHumanErrors = map[BridgeErrorCode]string{
	WALoggedOut:        "You were logged out from another device. Relogin to continue using the bridge.",
	WAMainDeviceGone:   "Your phone was logged out from WhatsApp. Relogin to continue using the bridge.",
	WAUnknownLogout:    "You were logged out for an unknown reason. Relogin to continue using the bridge.",
	WANotConnected:     "You're not connected to WhatsApp",
	WAConnecting:       "Reconnecting to WhatsApp...",
	WAKeepaliveTimeout: "The WhatsApp web servers are not responding. The bridge will try to reconnect.",
	WAPhoneOffline:     "Your phone hasn't been seen in over 12 days. The bridge is currently connected, but will get disconnected if you don't open the app soon.",
	WAConnectionFailed: "Connecting to the WhatsApp web servers failed.",
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

	Reason string                 `json:"reason,omitempty"`
	Info   map[string]interface{} `json:"info,omitempty"`
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

func (br *WABridge) sendBridgeState(ctx context.Context, state *BridgeState) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(&state); err != nil {
		return fmt.Errorf("failed to encode bridge state JSON: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, br.Config.Homeserver.StatusEndpoint, &body)
	if err != nil {
		return fmt.Errorf("failed to prepare request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+br.Config.AppService.ASToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(resp.Body)
		if respBody != nil {
			respBody = bytes.ReplaceAll(respBody, []byte("\n"), []byte("\\n"))
		}
		return fmt.Errorf("unexpected status code %d sending bridge state update: %s", resp.StatusCode, respBody)
	}
	return nil
}

func (br *WABridge) sendGlobalBridgeState(state BridgeState) {
	if len(br.Config.Homeserver.StatusEndpoint) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := br.sendBridgeState(ctx, &state); err != nil {
		br.Log.Warnln("Failed to update global bridge state:", err)
	} else {
		br.Log.Debugfln("Sent new global bridge state %+v", state)
	}
}

func (user *User) bridgeStateLoop() {
	defer func() {
		err := recover()
		if err != nil {
			user.log.Errorfln("Bridge state loop panicked: %v\n%s", err, debug.Stack())
		}
	}()
	for state := range user.bridgeStateQueue {
		user.immediateSendBridgeState(state)
	}
}

func (user *User) immediateSendBridgeState(state BridgeState) {
	retryIn := 2
	for {
		if user.prevBridgeStatus != nil && user.prevBridgeStatus.shouldDeduplicate(&state) {
			user.log.Debugfln("Not sending bridge state %s as it's a duplicate", state.StateEvent)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := user.bridge.sendBridgeState(ctx, &state)
		cancel()

		if err != nil {
			user.log.Warnfln("Failed to update bridge state: %v, retrying in %d seconds", err, retryIn)
			time.Sleep(time.Duration(retryIn) * time.Second)
			retryIn *= 2
			if retryIn > 64 {
				retryIn = 64
			}
		} else {
			user.prevBridgeStatus = &state
			user.log.Debugfln("Sent new bridge state %+v", state)
			return
		}
	}
}

func (user *User) sendBridgeState(state BridgeState) {
	if len(user.bridge.Config.Homeserver.StatusEndpoint) == 0 {
		return
	}

	state = state.fill(user)

	if len(user.bridgeStateQueue) >= 8 {
		user.log.Warnln("Bridge state queue is nearly full, discarding an item")
		select {
		case <-user.bridgeStateQueue:
		default:
		}
	}
	select {
	case user.bridgeStateQueue <- state:
	default:
		user.log.Errorfln("Bridge state queue is full, dropped new state")
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
