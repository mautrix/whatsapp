package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*WhatsAppClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*WhatsAppClient)(nil)
)

var (
	ErrInputLooksLikeEmail = bridgev2.WrapRespErr(errors.New("WhatsApp only supports phone numbers as user identifiers. Number looks like email"), mautrix.MInvalidParam)
)

func looksEmaily(str string) bool {
	for _, char := range str {
		// Characters that are usually in emails, but shouldn't be in phone numbers
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '@' {
			return true
		}
	}
	return false
}

func (wa *WhatsAppClient) validateIdentifer(number string) (types.JID, error) {
	if strings.HasSuffix(number, "@"+types.DefaultUserServer) {
		jid, _ := types.ParseJID(number)
		number = "+" + jid.User
	}
	if looksEmaily(number) {
		return types.EmptyJID, ErrInputLooksLikeEmail
	} else if wa.Client == nil || !wa.Client.IsLoggedIn() {
		return types.EmptyJID, bridgev2.ErrNotLoggedIn
	} else if resp, err := wa.Client.IsOnWhatsApp([]string{number}); err != nil {
		return types.EmptyJID, fmt.Errorf("failed to check if number is on WhatsApp: %w", err)
	} else if len(resp) == 0 {
		return types.EmptyJID, fmt.Errorf("the server did not respond to the query")
	} else if !resp[0].IsIn {
		return types.EmptyJID, bridgev2.WrapRespErr(fmt.Errorf("the server said +%s is not on WhatsApp", resp[0].JID.User), mautrix.MNotFound)
	} else {
		return resp[0].JID, nil
	}
}

func (wa *WhatsAppClient) ResolveIdentifier(ctx context.Context, identifier string, startChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	jid, err := wa.validateIdentifer(identifier)
	if err != nil {
		return nil, err
	}
	ghost, err := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(jid))
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}

	return &bridgev2.ResolveIdentifierResponse{
		Ghost:  ghost,
		UserID: waid.MakeUserID(jid),
		Chat:   &bridgev2.CreateChatResponse{PortalKey: wa.makeWAPortalKey(jid)},
	}, nil
}

func (wa *WhatsAppClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	contacts, err := wa.Client.Store.Contacts.GetAllContacts()
	if err != nil {
		return nil, err
	}
	resp := make([]*bridgev2.ResolveIdentifierResponse, len(contacts))
	i := 0
	for jid, contactInfo := range contacts {
		ghost, _ := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(jid))
		resp[i] = &bridgev2.ResolveIdentifierResponse{
			Ghost:    ghost,
			UserID:   waid.MakeUserID(jid),
			UserInfo: wa.contactToUserInfo(jid, contactInfo, false),
			Chat:     &bridgev2.CreateChatResponse{PortalKey: wa.makeWAPortalKey(jid)},
		}
		i++
	}
	return resp, nil
}
