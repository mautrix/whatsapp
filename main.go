// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

package main

import (
	"context"
	_ "embed"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	"go.mau.fi/util/configupgrade"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

//go:embed example-config.yaml
var ExampleConfig string

type WABridge struct {
	bridge.Bridge
	Config       *config.Config
	DB           *database.Database
	Provisioning *ProvisioningAPI
	Formatter    *Formatter
	Metrics      *MetricsHandler
	WAContainer  *sqlstore.Container
	WAVersion    string

	usersByMXID         map[id.UserID]*User
	usersByUsername     map[string]*User
	usersLock           sync.Mutex
	spaceRooms          map[id.RoomID]*User
	spaceRoomsLock      sync.Mutex
	managementRooms     map[id.RoomID]*User
	managementRoomsLock sync.Mutex
	portalsByMXID       map[id.RoomID]*Portal
	portalsByJID        map[database.PortalKey]*Portal
	portalsLock         sync.Mutex
	puppets             map[types.JID]*Puppet
	puppetsByCustomMXID map[id.UserID]*Puppet
	puppetsLock         sync.Mutex
}

func (br *WABridge) Init() {
	br.CommandProcessor = commands.NewProcessor(&br.Bridge)
	br.RegisterCommands()

	// TODO this is a weird place for this
	br.EventProcessor.On(event.EphemeralEventPresence, br.HandlePresence)
	br.EventProcessor.On(TypeMSC3381PollStart, br.MatrixHandler.HandleMessage)
	br.EventProcessor.On(TypeMSC3381PollResponse, br.MatrixHandler.HandleMessage)
	br.EventProcessor.On(TypeMSC3381V2PollResponse, br.MatrixHandler.HandleMessage)

	Analytics.log = br.ZLog.With().Str("component", "analytics").Logger()
	Analytics.url = (&url.URL{
		Scheme: "https",
		Host:   br.Config.Analytics.Host,
		Path:   "/v1/track",
	}).String()
	Analytics.key = br.Config.Analytics.Token
	Analytics.userID = br.Config.Analytics.UserID
	if Analytics.IsEnabled() {
		Analytics.log.Info().Str("override_user_id", Analytics.userID).Msg("Analytics metrics are enabled")
	}

	br.DB = database.New(br.Bridge.DB)
	br.WAContainer = sqlstore.NewWithDB(br.DB.RawDB, br.DB.Dialect.String(), waLog.Zerolog(br.ZLog.With().Str("db_section", "whatsmeow").Logger()))
	br.WAContainer.DatabaseErrorHandler = br.DB.HandleSignalStoreError

	ss := br.Config.Bridge.Provisioning.SharedSecret
	if len(ss) > 0 && ss != "disable" {
		br.Provisioning = &ProvisioningAPI{bridge: br, log: br.ZLog.With().Str("component", "provisioning").Logger()}
	}

	br.Formatter = NewFormatter(br)
	br.Metrics = NewMetricsHandler(br.Config.Metrics.Listen, br.ZLog.With().Str("component", "metrics").Logger(), br.DB)
	br.MatrixHandler.TrackEventDuration = br.Metrics.TrackMatrixEvent

	store.BaseClientPayload.UserAgent.OsVersion = proto.String(br.WAVersion)
	store.BaseClientPayload.UserAgent.OsBuildNumber = proto.String(br.WAVersion)
	store.DeviceProps.Os = proto.String(br.Config.WhatsApp.OSName)
	store.DeviceProps.RequireFullSync = proto.Bool(br.Config.Bridge.HistorySync.RequestFullSync)
	if fsc := br.Config.Bridge.HistorySync.FullSyncConfig; fsc.DaysLimit > 0 && fsc.SizeLimit > 0 && fsc.StorageQuota > 0 {
		store.DeviceProps.HistorySyncConfig = &waProto.DeviceProps_HistorySyncConfig{
			FullSyncDaysLimit:   proto.Uint32(fsc.DaysLimit),
			FullSyncSizeMbLimit: proto.Uint32(fsc.SizeLimit),
			StorageQuotaMb:      proto.Uint32(fsc.StorageQuota),
		}
	}
	versionParts := strings.Split(br.WAVersion, ".")
	if len(versionParts) > 2 {
		primary, _ := strconv.Atoi(versionParts[0])
		secondary, _ := strconv.Atoi(versionParts[1])
		tertiary, _ := strconv.Atoi(versionParts[2])
		store.DeviceProps.Version.Primary = proto.Uint32(uint32(primary))
		store.DeviceProps.Version.Secondary = proto.Uint32(uint32(secondary))
		store.DeviceProps.Version.Tertiary = proto.Uint32(uint32(tertiary))
	}
	platformID, ok := waCompanionReg.DeviceProps_PlatformType_value[strings.ToUpper(br.Config.WhatsApp.BrowserName)]
	if ok {
		store.DeviceProps.PlatformType = waProto.DeviceProps_PlatformType(platformID).Enum()
	}
}

func (br *WABridge) Start() {
	err := br.WAContainer.Upgrade()
	if err != nil {
		br.ZLog.WithLevel(zerolog.FatalLevel).Err(err).Msg("Failed to upgrade whatsmeow database")
		os.Exit(15)
	}
	if br.Provisioning != nil {
		br.Provisioning.Init()
	}
	// TODO find out how the new whatsapp version checks for updates
	ver, err := whatsmeow.GetLatestVersion(br.AS.HTTPClient)
	if err != nil {
		br.ZLog.Err(err).Msg("Failed to get latest WhatsApp web version number")
	} else {
		br.ZLog.Debug().
			Stringer("hardcoded_version", store.GetWAVersion()).
			Stringer("latest_version", *ver).
			Msg("Got latest WhatsApp web version number")
		store.SetWAVersion(*ver)
	}
	br.WaitWebsocketConnected()
	go br.StartUsers()
	if br.Config.Metrics.Enabled {
		go br.Metrics.Start()
	}

	go br.Loop()
}

func (br *WABridge) Loop() {
	ctx := br.ZLog.With().Str("action", "background loop").Logger().WithContext(context.TODO())
	for {
		br.SleepAndDeleteUpcoming(ctx)
		time.Sleep(1 * time.Hour)
		br.WarnUsersAboutDisconnection()
	}
}

func (br *WABridge) WarnUsersAboutDisconnection() {
	br.usersLock.Lock()
	for _, user := range br.usersByUsername {
		if user.IsConnected() && !user.PhoneRecentlySeen(true) {
			go user.sendPhoneOfflineWarning(context.TODO())
		}
	}
	br.usersLock.Unlock()
}

func (br *WABridge) StartUsers() {
	br.ZLog.Debug().Msg("Starting users")
	foundAnySessions := false
	for _, user := range br.GetAllUsers() {
		if !user.JID.IsEmpty() {
			foundAnySessions = true
		}
		go user.Connect()
	}
	if !foundAnySessions {
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured}.Fill(nil))
	}
	br.ZLog.Debug().Msg("Starting custom puppets")
	for _, loopuppet := range br.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			puppet.zlog.Debug().Stringer("custom_mxid", puppet.CustomMXID).Msg("Starting double puppet")
			err := puppet.StartCustomMXID(true)
			if err != nil {
				puppet.zlog.Err(err).Stringer("custom_mxid", puppet.CustomMXID).Msg("Failed to start double puppet")
			}
		}(loopuppet)
	}
}

func (br *WABridge) Stop() {
	br.Metrics.Stop()
	for _, user := range br.usersByUsername {
		if user.Client == nil {
			continue
		}
		user.zlog.Debug().Msg("Disconnecting user")
		user.Client.Disconnect()
		close(user.historySyncs)
	}
}

func (br *WABridge) GetExampleConfig() string {
	return ExampleConfig
}

func (br *WABridge) GetConfigPtr() interface{} {
	br.Config = &config.Config{
		BaseConfig: &br.Bridge.Config,
	}
	br.Config.BaseConfig.Bridge = &br.Config.Bridge
	return br.Config
}

func main() {
	br := &WABridge{
		usersByMXID:         make(map[id.UserID]*User),
		usersByUsername:     make(map[string]*User),
		spaceRooms:          make(map[id.RoomID]*User),
		managementRooms:     make(map[id.RoomID]*User),
		portalsByMXID:       make(map[id.RoomID]*Portal),
		portalsByJID:        make(map[database.PortalKey]*Portal),
		puppets:             make(map[types.JID]*Puppet),
		puppetsByCustomMXID: make(map[id.UserID]*Puppet),
	}
	br.Bridge = bridge.Bridge{
		Name:              "mautrix-whatsapp",
		URL:               "https://github.com/mautrix/whatsapp",
		Description:       "A Matrix-WhatsApp puppeting bridge.",
		Version:           "0.10.7",
		ProtocolName:      "WhatsApp",
		BeeperServiceName: "whatsapp",
		BeeperNetworkName: "whatsapp",

		CryptoPickleKey: "maunium.net/go/mautrix-whatsapp",

		ConfigUpgrader: &configupgrade.StructUpgrader{
			SimpleUpgrader: configupgrade.SimpleUpgrader(config.DoUpgrade),
			Blocks:         config.SpacedBlocks,
			Base:           ExampleConfig,
		},

		Child: br,
	}
	br.InitVersion(Tag, Commit, BuildTime)
	br.WAVersion = strings.FieldsFunc(br.Version, func(r rune) bool { return r == '-' || r == '+' })[0]

	br.Main()
}
