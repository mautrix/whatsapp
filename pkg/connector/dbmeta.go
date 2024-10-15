package connector

import (
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Ghost: func() any {
			return &waid.GhostMetadata{}
		},
		Message: func() any {
			return &waid.MessageMetadata{}
		},
		Reaction: func() any {
			return &waid.ReactionMetadata{}
		},
		Portal: func() any {
			return &waid.PortalMetadata{}
		},
		UserLogin: func() any {
			return &waid.UserLoginMetadata{}
		},
	}
}
