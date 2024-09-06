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
