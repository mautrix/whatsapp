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
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	whatsmeowUpgrades "go.mau.fi/whatsmeow/store/sqlstore/upgrades"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
)

type WhatsAppConnector struct {
	Bridge      *bridgev2.Bridge
	Config      Config
	DeviceStore *sqlstore.Container
	MsgConv     *msgconv.MessageConverter
	DB          *wadb.Database

	firstClientConnectOnce sync.Once
	backgroundConnectOnce  sync.Once

	mediaEditCache         MediaEditCache
	mediaEditCacheLock     sync.RWMutex
	stopMediaEditCacheLoop atomic.Pointer[context.CancelFunc]
}

var (
	_ bridgev2.NetworkConnector      = (*WhatsAppConnector)(nil)
	_ bridgev2.MaxFileSizeingNetwork = (*WhatsAppConnector)(nil)
	_ bridgev2.StoppableNetwork      = (*WhatsAppConnector)(nil)
)

func (wa *WhatsAppConnector) SetMaxFileSize(maxSize int64) {
	wa.MsgConv.MaxFileSize = maxSize
}

func (wa *WhatsAppConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "WhatsApp",
		NetworkURL:           "https://whatsapp.com",
		NetworkIcon:          "mxc://maunium.net/NeXNQarUbrlYBiPCpprYsRqr",
		NetworkID:            "whatsapp",
		BeeperBridgeType:     "whatsapp",
		DefaultPort:          29318,
		DefaultCommandPrefix: "!wa",
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
		cmdAccept, cmdSync, cmdInviteLink, cmdResolveLink, cmdJoin,
	)
	wa.mediaEditCache = make(MediaEditCache)

	whatsmeowDBLog := bridge.Log.With().Str("db_section", "whatsmeow").Logger()
	wa.DeviceStore = sqlstore.NewWithWrappedDB(
		bridge.DB.Child(
			"whatsmeow_version",
			whatsmeowUpgrades.Table,
			dbutil.ZeroLogger(whatsmeowDBLog),
		),
		waLog.Zerolog(whatsmeowDBLog),
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
	err := wa.DeviceStore.Upgrade(ctx)
	if err != nil {
		return bridgev2.DBUpgradeError{Err: err, Section: "whatsmeow"}
	}
	if !wa.Bridge.Background {
		err = wa.DeviceStore.LIDMap.FillCache(ctx)
		if err != nil {
			return fmt.Errorf("failed to fill LID cache: %w", err)
		}
	}
	err = wa.DB.Upgrade(ctx)
	if err != nil {
		return bridgev2.DBUpgradeError{Err: err, Section: "whatsapp"}
	}

	return nil
}

func (wa *WhatsAppConnector) Stop() {
	if stop := wa.stopMediaEditCacheLoop.Swap(nil); stop != nil {
		(*stop)()
	}
}

const kvWAVersion = "whatsapp_web_version"

func (wa *WhatsAppConnector) onFirstBackgroundConnect() {
	verStr := wa.Bridge.DB.KV.Get(wa.Bridge.BackgroundCtx, kvWAVersion)
	if verStr == "" {
		wa.Bridge.Log.Warn().Msg("No WhatsApp web version number cached in database")
		return
	}
	ver, err := store.ParseVersion(verStr)
	if err != nil {
		wa.Bridge.Log.Err(err).Msg("Failed to parse WhatsApp web version number from database")
		return
	}
	wa.Bridge.Log.Debug().
		Stringer("hardcoded_version", store.GetWAVersion()).
		Stringer("cached_version", ver).
		Msg("Using cached WhatsApp web version number")
	store.SetWAVersion(ver)
}

func (wa *WhatsAppConnector) onFirstClientConnect() {
	ctx := wa.Bridge.BackgroundCtx
	ver, err := whatsmeow.GetLatestVersion(ctx, nil)
	if err != nil {
		wa.Bridge.Log.Err(err).Msg("Failed to get latest WhatsApp web version number")
	} else {
		wa.Bridge.Log.Debug().
			Stringer("hardcoded_version", store.GetWAVersion()).
			Stringer("latest_version", *ver).
			Msg("Got latest WhatsApp web version number")
		store.SetWAVersion(*ver)
		wa.Bridge.DB.KV.Set(ctx, kvWAVersion, ver.String())
	}
	meclCtx, cancel := context.WithCancel(ctx)
	wa.stopMediaEditCacheLoop.Store(&cancel)
	go wa.mediaEditCacheExpireLoop(meclCtx)
}

func (wa *WhatsAppConnector) GenerateTransactionID(_ id.UserID, _ id.RoomID, _ event.Type) networkid.RawTransactionID {
	// The "proper" way would be a hash of the user ID among other things, but the hash includes random bytes too,
	// so nobody can tell the difference if we just generate random bytes.
	return networkid.RawTransactionID(whatsmeow.WebMessageIDPrefix + strings.ToUpper(hex.EncodeToString(random.Bytes(9))))
}
