package connector

import (
	"context"
	"fmt"

	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

var moderatorPL = 50

var (
	_ bridgev2.ContactListingNetworkAPI = (*WhatsAppClient)(nil)
)

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

	avatar, err := wa.Client.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{
		Preview:     false,
		IsCommunity: false,
	})

	if avatar != nil && err == nil {
		ui.Avatar = &bridgev2.Avatar{
			ID: networkid.AvatarID(avatar.ID),
			Get: func(ctx context.Context) ([]byte, error) {
				return wa.Client.DownloadMediaWithPath(avatar.DirectPath, nil, nil, nil, 0, "", "")
			},
		}
	}

	return ui
}

func (wa *WhatsAppClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	contacts, err := wa.Client.Store.Contacts.GetAllContacts()
	if err != nil {
		return nil, err
	}
	resp := make([]*bridgev2.ResolveIdentifierResponse, len(contacts))
	index := 0
	for jid := range contacts {
		contact := contacts[jid]
		contactResp := &bridgev2.ResolveIdentifierResponse{
			UserInfo: wa.contactToUserInfo(jid, contact),
			//Chat:     wa.makeCreateDMResponse(contact), TODO implement
		}
		contactResp.UserID = waid.MakeUserID(&jid)
		ghost, err := wa.Main.Bridge.GetGhostByID(ctx, contactResp.UserID)
		if err != nil {
			return nil, fmt.Errorf("failed to get ghost for %s: %w", jid, err)
		}
		contactResp.Ghost = ghost

		resp[index] = contactResp
		index++
	}
	return resp, nil
}
