// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
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

package whatsapp_ext

import (
	"github.com/Rhymen/go-whatsapp"
	"encoding/json"
	"strings"
)

type MsgInfoCommand string

const (
	MsgInfoCommandAcknowledge MsgInfoCommand = "ack"
)

type MsgInfo struct {
	Command         MsgInfoCommand `json:"cmd"`
	ID              string         `json:"id"`
	Acknowledgement int            `json:"ack"`
	MessageFromJID  string         `json:"from"`
	SenderJID       string         `json:"participant"`
	ToJID           string         `json:"to"`
	Timestamp       int64          `json:"t"`
}

type MsgInfoHandler interface {
	whatsapp.Handler
	HandleMsgInfo(MsgInfo)
}

func (ext *ExtendedConn) handleMessageMsgInfo(message []byte) {
	var event MsgInfo
	err := json.Unmarshal(message, &event)
	if err != nil {
		ext.jsonParseError(err)
		return
	}
	event.MessageFromJID = strings.Replace(event.MessageFromJID, OldUserSuffix, NewUserSuffix, 1)
	event.SenderJID = strings.Replace(event.SenderJID, OldUserSuffix, NewUserSuffix, 1)
	event.ToJID = strings.Replace(event.ToJID, OldUserSuffix, NewUserSuffix, 1)
	for _, handler := range ext.handlers {
		msgInfoHandler, ok := handler.(MsgInfoHandler)
		if !ok {
			continue
		}
		msgInfoHandler.HandleMsgInfo(event)
	}
}
