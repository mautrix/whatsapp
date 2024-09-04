package connector

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*WhatsAppClient)(nil)
)

var (
	LooksLikeEmailErr = fmt.Errorf("WhatsApp only supports phone numbers as user identifiers. Number looks like email")
	NotLoggedInErr    = fmt.Errorf("User is not logged in to WhatsApp. No session")
	NoResponseErr     = fmt.Errorf("Didn't get a response to checking if the number is on WhatsApp. Error checking number")
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

func (wa *WhatsAppClient) ValidateIdentifer(number string) (types.JID, error) {
	if strings.HasSuffix(number, "@"+types.DefaultUserServer) {
		jid, _ := types.ParseJID(number)
		number = "+" + jid.User
	}
	if looksEmaily(number) {
		return types.EmptyJID, LooksLikeEmailErr
	} else if !wa.Client.IsLoggedIn() {
		return types.EmptyJID, NotLoggedInErr
	} else if resp, err := wa.Client.IsOnWhatsApp([]string{number}); err != nil {
		return types.EmptyJID, fmt.Errorf("Failed to check if number is on WhatsApp: %v", err)
	} else if len(resp) == 0 {
		return types.EmptyJID, NoResponseErr
	} else if !resp[0].IsIn {
		return types.EmptyJID, fmt.Errorf("The server said +%s is not on WhatsApp", resp[0].JID.User)
	} else {
		return resp[0].JID, nil
	}
}

func (wa *WhatsAppClient) ResolveIdentifier(_ context.Context, identifier string, _ bool) (*bridgev2.ResolveIdentifierResponse, error) {
	jid, err := wa.ValidateIdentifer(identifier)

	if err != nil {
		return nil, fmt.Errorf("failed to parse identifier: %w", err)
	}

	return &bridgev2.ResolveIdentifierResponse{
		UserID: waid.MakeUserID(&jid),
	}, nil
}
