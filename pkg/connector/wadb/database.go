package wadb

import (
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb/upgrades"
)

type Database struct {
	*dbutil.Database
	Conversation *ConversationQuery
	Message      *MessageQuery
	PollOption   *PollOptionQuery
	MediaRequest *MediaRequestQuery
}

func New(bridgeID networkid.BridgeID, db *dbutil.Database, log zerolog.Logger) *Database {
	db = db.Child("whatsapp_version", upgrades.Table, dbutil.ZeroLogger(log))
	return &Database{
		Database: db,
		Conversation: &ConversationQuery{
			BridgeID: bridgeID,
			QueryHelper: dbutil.MakeQueryHelper(db, func(_ *dbutil.QueryHelper[*Conversation]) *Conversation {
				return &Conversation{}
			}),
		},
		Message: &MessageQuery{
			BridgeID: bridgeID,
			Database: db,
		},
		PollOption: &PollOptionQuery{
			BridgeID: bridgeID,
			Database: db,
		},
		MediaRequest: &MediaRequestQuery{
			BridgeID: bridgeID,
			QueryHelper: dbutil.MakeQueryHelper(db, func(_ *dbutil.QueryHelper[*MediaRequest]) *MediaRequest {
				return &MediaRequest{}
			}),
		},
	}
}
