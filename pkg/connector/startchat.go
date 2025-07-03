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
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*WhatsAppClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*WhatsAppClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*WhatsAppClient)(nil)
	_ bridgev2.GhostDMCreatingNetworkAPI     = (*WhatsAppClient)(nil)
	_ bridgev2.IdentifierValidatingNetwork   = (*WhatsAppConnector)(nil)
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
	if strings.HasSuffix(number, "@"+types.BotServer) || strings.HasSuffix(number, "@"+types.HiddenUserServer) {
		return types.ParseJID(number)
	}
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

func isOnlyNumbers(user string) bool {
	for _, char := range user {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (wa *WhatsAppConnector) ValidateUserID(id networkid.UserID) bool {
	jid := waid.ParseUserID(id)
	switch jid.Server {
	case types.DefaultUserServer:
		return len(jid.User) <= 13 && (jid.User == "0" || len(jid.User) >= 7) && isOnlyNumbers(jid.User)
	case types.HiddenUserServer, types.BotServer:
		return len(jid.User) >= 12
	default:
		return false
	}
}

func (wa *WhatsAppClient) startChatLIDToPN(ctx context.Context, jid types.JID) (types.JID, error) {
	if jid.Server == types.HiddenUserServer {
		pn, err := wa.Device.LIDs.GetPNForLID(ctx, jid)
		if err != nil {
			return jid, fmt.Errorf("failed to get phone number for lid: %w", err)
		} else if pn.IsEmpty() {
			// Don't allow starting chats with LIDs for now
			return jid, fmt.Errorf("phone number not found")
		}
		return pn, nil
	}
	return jid, nil
}

func (wa *WhatsAppClient) makeCreateChatResponse(ctx context.Context, jid, origJID types.JID) *bridgev2.CreateChatResponse {
	var redirID networkid.UserID
	if origJID != jid {
		redirID = waid.MakeUserID(jid)
	}
	return &bridgev2.CreateChatResponse{
		PortalKey:      wa.makeWAPortalKey(jid),
		PortalInfo:     wa.wrapDMInfo(ctx, jid),
		DMRedirectedTo: redirID,
	}
}

func (wa *WhatsAppClient) CreateChatWithGhost(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.CreateChatResponse, error) {
	origJID := waid.ParseUserID(ghost.ID)
	jid, err := wa.startChatLIDToPN(ctx, origJID)
	if err != nil {
		return nil, err
	}
	return wa.makeCreateChatResponse(ctx, jid, origJID), nil
}

func (wa *WhatsAppClient) ResolveIdentifier(ctx context.Context, identifier string, startChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	origJID, err := wa.validateIdentifer(identifier)
	if err != nil {
		return nil, err
	}
	jid, err := wa.startChatLIDToPN(ctx, origJID)
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
		Chat:   wa.makeCreateChatResponse(ctx, jid, origJID),
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
	if !wa.IsLoggedIn() {
		return nil, mautrix.MForbidden.WithMessage("You must be logged in to list contacts")
	}
	contacts, err := wa.GetStore().Contacts.GetAllContacts(ctx)
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
			UserInfo: wa.contactToUserInfo(ctx, jid, contactInfo, false),
			Chat:     &bridgev2.CreateChatResponse{PortalKey: wa.makeWAPortalKey(jid)},
		})
	}
	return resp, nil
}
