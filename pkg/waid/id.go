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

const LIDPrefix = "lid-"
const BotPrefix = "bot-"

func MakeUserID(user types.JID) networkid.UserID {
	switch user.Server {
	case types.DefaultUserServer:
		return networkid.UserID(user.User)
	case types.BotServer:
		return networkid.UserID(BotPrefix + user.User)
	case types.HiddenUserServer:
		return networkid.UserID(LIDPrefix + user.User)
	default:
		return ""
	}
}

func ParseUserID(user networkid.UserID) types.JID {
	if strings.HasPrefix(string(user), LIDPrefix) {
		return types.NewJID(strings.TrimPrefix(string(user), LIDPrefix), types.HiddenUserServer)
	} else if strings.HasPrefix(string(user), BotPrefix) {
		return types.NewJID(strings.TrimPrefix(string(user), BotPrefix), types.BotServer)
	}
	return types.NewJID(string(user), types.DefaultUserServer)
}

func MakeUserLoginID(user types.JID) networkid.UserLoginID {
	if user.Server != types.DefaultUserServer {
		return ""
	}
	return networkid.UserLoginID(user.User)
}

func ParseUserLoginID(user networkid.UserLoginID, deviceID uint16) types.JID {
	if user == "" {
		return types.EmptyJID
	}
	return types.JID{
		Server: types.DefaultUserServer,
		User:   string(user),
		Device: deviceID,
	}
}

func MakeMessageID(chat, sender types.JID, id types.MessageID) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("%s:%s:%s", chat.ToNonAD().String(), sender.ToNonAD().String(), id))
}

func MakeFakeMessageID(chat, sender types.JID, data string) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("fake:%s:%s:%s", chat.ToNonAD().String(), sender.ToNonAD().String(), data))
}

type ParsedMessageID struct {
	Chat   types.JID
	Sender types.JID
	ID     types.MessageID
}

func (pmi *ParsedMessageID) String() networkid.MessageID {
	return MakeMessageID(pmi.Chat, pmi.Sender, pmi.ID)
}

func ParseMessageID(messageID networkid.MessageID) (*ParsedMessageID, error) {
	parts := strings.SplitN(string(messageID), ":", 3)
	if len(parts) == 3 {
		if parts[0] == "fake" || strings.HasPrefix(parts[2], "FAKE::") {
			return nil, fmt.Errorf("fake message ID")
		}
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
		return nil, fmt.Errorf("invalid message ID")
	}
}
