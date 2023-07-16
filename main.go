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
	_ "embed"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/configupgrade"

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

	Segment.log = br.Log.Sub("Segment")
	Segment.key = br.Config.SegmentKey
	Segment.userID = br.Config.SegmentUserID
	if Segment.IsEnabled() {
		Segment.log.Infoln("Segment metrics are enabled")
		if Segment.userID != "" {
			Segment.log.Infoln("Overriding Segment user_id with %v", Segment.userID)
		}
	}

	br.DB = database.New(br.Bridge.DB, br.Log.Sub("Database"))
	br.WAContainer = sqlstore.NewWithDB(br.DB.RawDB, br.DB.Dialect.String(), &waLogger{br.Log.Sub("Database").Sub("WhatsApp")})
	br.WAContainer.DatabaseErrorHandler = br.DB.HandleSignalStoreError

	ss := br.Config.Bridge.Provisioning.SharedSecret
	if len(ss) > 0 && ss != "disable" {
		br.Provisioning = &ProvisioningAPI{bridge: br}
	}

	br.Formatter = NewFormatter(br)
	br.Metrics = NewMetricsHandler(br.Config.Metrics.Listen, br.Log.Sub("Metrics"), br.DB)
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
	platformID, ok := waProto.DeviceProps_PlatformType_value[strings.ToUpper(br.Config.WhatsApp.BrowserName)]
	if ok {
		store.DeviceProps.PlatformType = waProto.DeviceProps_PlatformType(platformID).Enum()
	}
}

func (br *WABridge) Start() {
	err := br.WAContainer.Upgrade()
	if err != nil {
		br.Log.Fatalln("Failed to upgrade whatsmeow database: %v", err)
		os.Exit(15)
	}
	if br.Provisioning != nil {
		br.Log.Debugln("Initializing provisioning API")
		br.Provisioning.Init()
	}
	go br.CheckWhatsAppUpdate()
	br.WaitWebsocketConnected()
	go br.StartUsers()
	if br.Config.Metrics.Enabled {
		go br.Metrics.Start()
	}

	go br.Loop()
}

func (br *WABridge) CheckWhatsAppUpdate() {
	br.Log.Debugfln("Checking for WhatsApp web update")
	resp, err := whatsmeow.CheckUpdate(http.DefaultClient)
	if err != nil {
		br.Log.Warnfln("Failed to check for WhatsApp web update: %v", err)
		return
	}
	if store.GetWAVersion() == resp.ParsedVersion {
		br.Log.Debugfln("Bridge is using latest WhatsApp web protocol")
	} else if store.GetWAVersion().LessThan(resp.ParsedVersion) {
		if resp.IsBelowHard || resp.IsBroken {
			br.Log.Warnfln("Bridge is using outdated WhatsApp web protocol and probably doesn't work anymore (%s, latest is %s)", store.GetWAVersion(), resp.ParsedVersion)
		} else if resp.IsBelowSoft {
			br.Log.Infofln("Bridge is using outdated WhatsApp web protocol (%s, latest is %s)", store.GetWAVersion(), resp.ParsedVersion)
		} else {
			br.Log.Debugfln("Bridge is using outdated WhatsApp web protocol (%s, latest is %s)", store.GetWAVersion(), resp.ParsedVersion)
		}
	} else {
		br.Log.Debugfln("Bridge is using newer than latest WhatsApp web protocol")
	}
}

func (br *WABridge) Loop() {
	for {
		br.SleepAndDeleteUpcoming()
		time.Sleep(1 * time.Hour)
		br.WarnUsersAboutDisconnection()
	}
}

func (br *WABridge) WarnUsersAboutDisconnection() {
	br.usersLock.Lock()
	for _, user := range br.usersByUsername {
		if user.IsConnected() && !user.PhoneRecentlySeen(true) {
			go user.sendPhoneOfflineWarning()
		}
	}
	br.usersLock.Unlock()
}

func (br *WABridge) StartUsers() {
	br.Log.Debugln("Starting users")
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
	br.Log.Debugln("Starting custom puppets")
	for _, loopuppet := range br.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			puppet.log.Debugln("Starting custom puppet", puppet.CustomMXID)
			err := puppet.StartCustomMXID(true)
			if err != nil {
				puppet.log.Errorln("Failed to start custom puppet:", err)
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
		br.Log.Debugln("Disconnecting", user.MXID)
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
		Version:           "0.9.0",
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
