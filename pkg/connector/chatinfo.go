package connector

import (
	"context"

	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

var moderatorPL = 50

func (wa *WhatsAppClient) GetChatInfo(_ context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	myJID := wa.Client.Store.ID.ToNonAD()
	members := &bridgev2.ChatMemberList{
		IsFull: true,
		Members: []bridgev2.ChatMember{
			{
				EventSender: wa.makeEventSender(&myJID),
				Membership:  event.MembershipJoin,
				PowerLevel:  &moderatorPL,
			},
		},
	}

	jid, _ := types.ParseJID(string(portal.ID))
	if jid.Server == types.GroupServer {
		return &bridgev2.ChatInfo{}, nil
	}
	if networkid.UserLoginID(jid.User) != wa.UserLogin.ID {
		members.Members = append(members.Members, bridgev2.ChatMember{
			EventSender: wa.makeEventSender(&jid),
			Membership:  event.MembershipJoin,
		})
	}

	return &bridgev2.ChatInfo{
		Members: members,
		Type:    ptr.Ptr(database.RoomTypeDM),
	}, nil
}

func (wa *WhatsAppClient) GetUserInfo(_ context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	jid := types.NewJID(string(ghost.ID), types.DefaultUserServer)

	contact, err := wa.Client.Store.Contacts.GetContact(jid)
	if err != nil {
		return nil, err
	}

	// make user info from contact info
	return wa.contactToUserInfo(jid, contact), nil
}

func (wa *WhatsAppClient) contactToUserInfo(jid types.JID, contact types.ContactInfo) *bridgev2.UserInfo {
	isBot := false
	ui := &bridgev2.UserInfo{
		IsBot:       &isBot,
		Identifiers: []string{},
	}
	name := wa.Main.Config.FormatDisplayname(jid, contact)
	ui.Name = &name
	ui.Identifiers = append(ui.Identifiers, "tel:+"+jid.User)
	// TODO: implement Profile picture stuff
	return ui
}
