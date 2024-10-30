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
	_ bridgev2.UserSearchingNetworkAPI       = (*WhatsAppClient)(nil)
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
	return wa.getContactList(ctx, "")
}

func (wa *WhatsAppClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	return wa.getContactList(ctx, strings.ToLower(query))
}

func matchesQuery(str string, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(str), query)
}

func (wa *WhatsAppClient) getContactList(ctx context.Context, filter string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	contacts, err := wa.Client.Store.Contacts.GetAllContacts()
	if err != nil {
		return nil, err
	}
	resp := make([]*bridgev2.ResolveIdentifierResponse, 0, len(contacts))
	for jid, contactInfo := range contacts {
		if !matchesQuery(contactInfo.PushName, filter) && !matchesQuery(contactInfo.FullName, filter) && !matchesQuery(jid.User, filter) {
			continue
		}
		ghost, _ := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(jid))
		resp = append(resp, &bridgev2.ResolveIdentifierResponse{
			Ghost:    ghost,
			UserID:   waid.MakeUserID(jid),
			UserInfo: wa.contactToUserInfo(jid, contactInfo, false),
			Chat:     &bridgev2.CreateChatResponse{PortalKey: wa.makeWAPortalKey(jid)},
		})
	}
	return resp, nil
}
