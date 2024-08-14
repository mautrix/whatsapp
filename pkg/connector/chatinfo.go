package connector

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
)

func (wa *WhatsAppClient) GetChatInfo(_ context.Context, _ *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	return nil, nil
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
	ui.Identifiers = append(ui.Identifiers, "tel:"+jid.User)
	// TODO: implement Profile picture stuff
	return ui
}
