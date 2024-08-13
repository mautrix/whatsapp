package waid

import (
	"fmt"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func ParseWAPortalID(portal networkid.PortalID, server string) types.JID {
	return types.JID{
		User:   string(portal),
		Server: server,
	}
}

func MakeWAPortalID(jid types.JID) networkid.PortalID {
	return networkid.PortalID(jid.User)
}

func MakeWAUserID(jid *types.JID) networkid.UserID {
	return networkid.UserID(jid.User)
}

func ParseWAUserLoginID(user networkid.UserLoginID) types.JID {
	return types.JID{
		Server: types.DefaultUserServer,
		User:   string(user),
	}
}

func MakeWAUserLoginID(jid *types.JID) networkid.UserLoginID {
	return networkid.UserLoginID(jid.User)
}

func MakeUserID(user int64) networkid.UserID {
	return networkid.UserID(strconv.Itoa(int(user)))
}

func MakeUserLoginID(user int64) networkid.UserLoginID {
	return networkid.UserLoginID(MakeUserID(user))
}

func MakeMessageID(chat, sender types.JID, id types.MessageID) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("%s:%s:%s", chat.String(), sender.ToNonAD().String(), id))
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
