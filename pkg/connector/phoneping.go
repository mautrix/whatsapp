// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package connector

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/util/exfmt"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) disconnectWarningLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if wa.Client != nil && wa.Client.IsConnected() {
				if !wa.PhoneRecentlySeen(true) {
					go wa.sendPhoneOfflineWarning(ctx)
				}
			}
		}
	}
}

func (wa *WhatsAppClient) sendPhoneOfflineWarning(ctx context.Context) {
	if wa.UserLogin.User.ManagementRoom == "" || time.Since(wa.lastPhoneOfflineWarning) < 12*time.Hour {
		// Don't spam the warning too much
		return
	}
	wa.lastPhoneOfflineWarning = time.Now()
	timeSinceSeen := time.Since(wa.UserLogin.Metadata.(*waid.UserLoginMetadata).PhoneLastSeen.Time).Round(time.Hour)
	// TODO remove this manual message after bridge states are plumbed to the management room as messages
	_, _ = wa.Main.Bridge.Bot.SendMessage(ctx, wa.UserLogin.User.ManagementRoom, event.EventMessage, &event.Content{
		Parsed: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    fmt.Sprintf("Your phone hasn't been seen in %s. The server will force the bridge to log out if the phone is not active at least every 2 weeks.", exfmt.Duration(timeSinceSeen)),
		},
	}, nil)
}

const PhoneDisconnectWarningTime = 12 * 24 * time.Hour // 12 days
const PhoneDisconnectPingTime = 10 * 24 * time.Hour
const PhoneMinPingInterval = 24 * time.Hour

func (wa *WhatsAppClient) PhoneRecentlySeen(doPing bool) bool {
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	if doPing && !meta.PhoneLastSeen.IsZero() && time.Since(meta.PhoneLastSeen.Time) > PhoneDisconnectPingTime && time.Since(meta.PhoneLastPinged.Time) > PhoneMinPingInterval {
		// Over 10 days since the phone was seen and over a day since the last somewhat hacky ping, send a new ping.
		go wa.sendHackyPhonePing()
	}
	return meta.PhoneLastSeen.IsZero() || time.Since(meta.PhoneLastSeen.Time) < PhoneDisconnectWarningTime
}

const getUserLastAppStateKeyIDQuery = "SELECT key_id FROM whatsmeow_app_state_sync_keys WHERE jid=$1 ORDER BY timestamp DESC LIMIT 1"

func (wa *WhatsAppClient) sendHackyPhonePing() {
	log := wa.UserLogin.Log.With().Str("action", "hacky phone ping").Logger()
	ctx := log.WithContext(context.Background())
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	meta.PhoneLastPinged = jsontime.UnixNow()
	msgID := wa.Client.GenerateMessageID()
	keyIDs := make([]*waE2E.AppStateSyncKeyId, 0, 1)
	var lastKeyID []byte
	err := wa.Main.DB.QueryRow(ctx, getUserLastAppStateKeyIDQuery, wa.JID).Scan(&lastKeyID)
	if err != nil {
		log.Err(err).Msg("Failed to get last app state key ID to send hacky phone ping - sending empty request")
	} else if lastKeyID != nil {
		keyIDs = append(keyIDs, &waE2E.AppStateSyncKeyId{
			KeyID: lastKeyID,
		})
	}
	resp, err := wa.Client.SendMessage(ctx, wa.JID.ToNonAD(), &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_REQUEST.Enum(),
			AppStateSyncKeyRequest: &waE2E.AppStateSyncKeyRequest{
				KeyIDs: keyIDs,
			},
		},
	}, whatsmeow.SendRequestExtra{Peer: true, ID: msgID})
	if err != nil {
		log.Err(err).Msg("Failed to send hacky phone ping")
	} else {
		log.Debug().
			Str("message_id", msgID).
			Int64("message_ts", resp.Timestamp.Unix()).
			Msg("Sent hacky phone ping because phone has been offline for >10 days")
		meta.PhoneLastPinged = jsontime.U(resp.Timestamp)
		err = wa.UserLogin.Save(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save login metadata after sending hacky phone ping")
		}
	}
}

// phoneSeen records a timestamp when the user's main device was seen online.
// The stored timestamp can later be used to warn the user if the main device is offline for too long.
func (wa *WhatsAppClient) phoneSeen(ts time.Time) {
	log := wa.UserLogin.Log.With().Str("action", "phone seen").Time("seen_at", ts).Logger()
	ctx := log.WithContext(context.Background())
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	if meta.PhoneLastSeen.Add(1 * time.Hour).After(ts) {
		// The last seen timestamp isn't going to be perfectly accurate in any case,
		// so don't spam the database with an update every time there's an event.
		return
	} else if !wa.PhoneRecentlySeen(false) {
		isConnected := wa.IsLoggedIn() && wa.Client.IsConnected()
		prevStateError := wa.UserLogin.BridgeState.GetPrev().Error
		if prevStateError == WAPhoneOffline && isConnected {
			log.Debug().Msg("Saw phone after current bridge state said it has been offline, switching state back to connected")
			wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		} else {
			log.Debug().
				Bool("is_connected", isConnected).
				Str("prev_error", string(prevStateError)).
				Msg("Saw phone after current bridge state said it has been offline, not sending new bridge state")
		}
	}
	meta.PhoneLastSeen = jsontime.U(ts)
	go func() {
		err := wa.UserLogin.Save(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save user after updating phone last seen")
		}
	}()
}
