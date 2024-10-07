package connector

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"

	"maunium.net/go/mautrix-whatsapp/pkg/connector/wadb"
	"maunium.net/go/mautrix-whatsapp/pkg/msgconv"
)

type WhatsAppConnector struct {
	Bridge      *bridgev2.Bridge
	Config      Config
	DeviceStore *sqlstore.Container
	MsgConv     *msgconv.MessageConverter
	DB          *wadb.Database
}

var _ bridgev2.NetworkConnector = (*WhatsAppConnector)(nil)
var _ bridgev2.MaxFileSizeingNetwork = (*WhatsAppConnector)(nil)

func (wa *WhatsAppConnector) SetMaxFileSize(maxSize int64) {
	wa.MsgConv.MaxFileSize = maxSize
}

func (wa *WhatsAppConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "WhatsApp",
		NetworkURL:       "https://whatsapp.com",
		NetworkIcon:      "mxc://maunium.net/NeXNQarUbrlYBiPCpprYsRqr",
		NetworkID:        "whatsapp",
		BeeperBridgeType: "whatsapp",
		DefaultPort:      29318,
	}
}

func (wa *WhatsAppConnector) Init(bridge *bridgev2.Bridge) {
	wa.Bridge = bridge
	wa.MsgConv = msgconv.New(bridge)
	wa.MsgConv.AnimatedStickerConfig = wa.Config.AnimatedSticker
	wa.MsgConv.ExtEvPolls = wa.Config.ExtEvPolls
	wa.MsgConv.DisableViewOnce = wa.Config.DisableViewOnce
	wa.MsgConv.OldMediaSuffix = "Requesting old media is not enabled on this bridge."
	wa.MsgConv.FetchURLPreviews = wa.Config.URLPreviews
	if wa.Config.HistorySync.MediaRequests.AutoRequestMedia {
		if wa.Config.HistorySync.MediaRequests.RequestMethod == MediaRequestMethodImmediate {
			wa.MsgConv.OldMediaSuffix = "Media will be requested from your phone automatically soon."
		} else if wa.Config.HistorySync.MediaRequests.RequestMethod == MediaRequestMethodLocalTime {
			wa.MsgConv.OldMediaSuffix = "Media will be requested from your phone automatically overnight."
		}
	}
	wa.DB = wadb.New(bridge.ID, bridge.DB.Database, bridge.Log.With().Str("db_section", "whatsapp").Logger())
	wa.MsgConv.DB = wa.DB
	wa.Bridge.Commands.(*commands.Processor).AddHandlers(
		cmdAccept,
	)

	wa.DeviceStore = sqlstore.NewWithDB(
		bridge.DB.RawDB,
		bridge.DB.Dialect.String(),
		waLog.Zerolog(bridge.Log.With().Str("db_section", "whatsmeow").Logger()),
	)

	store.DeviceProps.Os = proto.String(wa.Config.OSName)
	store.DeviceProps.RequireFullSync = proto.Bool(wa.Config.HistorySync.RequestFullSync)
	if fsc := wa.Config.HistorySync.FullSyncConfig; fsc.DaysLimit > 0 && fsc.SizeLimit > 0 && fsc.StorageQuota > 0 {
		store.DeviceProps.HistorySyncConfig = &waCompanionReg.DeviceProps_HistorySyncConfig{
			FullSyncDaysLimit:   proto.Uint32(fsc.DaysLimit),
			FullSyncSizeMbLimit: proto.Uint32(fsc.SizeLimit),
			StorageQuotaMb:      proto.Uint32(fsc.StorageQuota),
		}
	}
	platformID, ok := waCompanionReg.DeviceProps_PlatformType_value[strings.ToUpper(wa.Config.BrowserName)]
	if ok {
		store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_PlatformType(platformID).Enum()
	}
}

func (wa *WhatsAppConnector) Start(ctx context.Context) error {
	err := wa.DeviceStore.Upgrade()
	if err != nil {
		return bridgev2.DBUpgradeError{Err: err, Section: "whatsmeow"}
	}
	err = wa.DB.Upgrade(ctx)
	if err != nil {
		return bridgev2.DBUpgradeError{Err: err, Section: "whatsapp"}
	}

	ver, err := whatsmeow.GetLatestVersion(nil)
	if err != nil {
		wa.Bridge.Log.Err(err).Msg("Failed to get latest WhatsApp web version number")
	} else {
		wa.Bridge.Log.Debug().
			Stringer("hardcoded_version", store.GetWAVersion()).
			Stringer("latest_version", *ver).
			Msg("Got latest WhatsApp web version number")
		store.SetWAVersion(*ver)
	}
	return nil
}
