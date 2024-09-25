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

package waid

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func MakePortalID(jid types.JID) networkid.PortalID {
	return networkid.PortalID(jid.ToNonAD().String())
}

func ParsePortalID(portal networkid.PortalID) (types.JID, error) {
	parsed, err := types.ParseJID(string(portal))
	if err != nil {
		return types.EmptyJID, fmt.Errorf("invalid portal ID: %w", err)
	}
	return parsed, nil
}

func MakeUserID(user types.JID) networkid.UserID {
	return networkid.UserID(user.User)
}

func ParseUserID(user networkid.UserID) types.JID {
	return types.NewJID(string(user), types.DefaultUserServer)
}

func MakeUserLoginID(user types.JID) networkid.UserLoginID {
	return networkid.UserLoginID(user.User)
}

func ParseUserLoginID(user networkid.UserLoginID, deviceID uint16) types.JID {
	return types.JID{
		Server: types.DefaultUserServer,
		User:   string(user),
		Device: deviceID,
	}
}

func MakeMessageID(chat, sender types.JID, id types.MessageID) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("%s:%s:%s", chat.ToNonAD().String(), sender.ToNonAD().String(), id))
}

type ParsedMessageID struct {
	Chat   types.JID
	Sender types.JID
	ID     types.MessageID
}

func ParseMessageID(messageID networkid.MessageID) (*ParsedMessageID, error) {
	parts := strings.SplitN(string(messageID), ":", 3)
	if len(parts) == 3 {
		chat, err := types.ParseJID(parts[0])
		if err != nil {
			return nil, err
		}
		sender, err := types.ParseJID(parts[1])
		if err != nil {
			return nil, err
		}
		return &ParsedMessageID{Chat: chat, Sender: sender, ID: parts[2]}, nil
	} else {
		return nil, fmt.Errorf("invalid message id")
	}
}
