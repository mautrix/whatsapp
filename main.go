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
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	flag "maunium.net/go/mauflag"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/database/upgrades"
)

// The name and repo URL of the bridge.
var (
	Name = "mautrix-whatsapp"
	URL  = "https://github.com/mautrix/whatsapp"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var (
	// Version is the version number of the bridge. Changed manually when making a release.
	Version = "0.3.0"
	// WAVersion is the version number exposed to WhatsApp. Filled in init()
	WAVersion = ""
	// VersionString is the bridge version, plus commit information. Filled in init() using the build-time values.
	VersionString = ""
)

//go:embed example-config.yaml
var ExampleConfig string

func init() {
	if len(Tag) > 0 && Tag[0] == 'v' {
		Tag = Tag[1:]
	}
	if Tag != Version {
		suffix := ""
		if !strings.HasSuffix(Version, "+dev") {
			suffix = "+dev"
		}
		if len(Commit) > 8 {
			Version = fmt.Sprintf("%s%s.%s", Version, suffix, Commit[:8])
		} else {
			Version = fmt.Sprintf("%s%s.unknown", Version, suffix)
		}
	}
	mautrix.DefaultUserAgent = fmt.Sprintf("mautrix-whatsapp/%s %s", Version, mautrix.DefaultUserAgent)
	WAVersion = strings.FieldsFunc(Version, func(r rune) bool { return r == '-' || r == '+' })[0]
	VersionString = fmt.Sprintf("%s %s (%s)", Name, Version, BuildTime)

	config.ExampleConfig = ExampleConfig
}

var configPath = flag.MakeFull("c", "config", "The path to your config file.", "config.yaml").String()
var dontSaveConfig = flag.MakeFull("n", "no-update", "Don't save updated config to disk.", "false").Bool()
var registrationPath = flag.MakeFull("r", "registration", "The path where to save the appservice registration.", "registration.yaml").String()
var generateRegistration = flag.MakeFull("g", "generate-registration", "Generate registration and quit.", "false").Bool()
var version = flag.MakeFull("v", "version", "View bridge version and quit.", "false").Bool()
var ignoreUnsupportedDatabase = flag.Make().LongKey("ignore-unsupported-database").Usage("Run even if the database schema is too new").Default("false").Bool()
var ignoreForeignTables = flag.Make().LongKey("ignore-foreign-tables").Usage("Run even if the database contains tables from other programs (like Synapse)").Default("false").Bool()
var migrateFrom = flag.Make().LongKey("migrate-db").Usage("Source database type and URI to migrate from.").Bool()
var wantHelp, _ = flag.MakeHelpFlag()

func (bridge *Bridge) GenerateRegistration() {
	if *dontSaveConfig {
		// We need to save the generated as_token and hs_token in the config
		_, _ = fmt.Fprintln(os.Stderr, "--no-update is not compatible with --generate-registration")
		os.Exit(5)
	}
	reg, err := bridge.Config.NewRegistration()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to generate registration:", err)
		os.Exit(20)
	}

	err = reg.Save(*registrationPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to save registration:", err)
		os.Exit(21)
	}

	err = config.Mutate(*configPath, func(helper *config.UpgradeHelper) {
		helper.Set(config.Str, bridge.Config.AppService.ASToken, "appservice", "as_token")
		helper.Set(config.Str, bridge.Config.AppService.HSToken, "appservice", "hs_token")
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to save config:", err)
		os.Exit(22)
	}
	fmt.Println("Registration generated. Add the path to the registration to your Synapse config, restart it, then start the bridge.")
	os.Exit(0)
}

func (bridge *Bridge) MigrateDatabase() {
	oldDB, err := database.New(config.DatabaseConfig{Type: flag.Arg(0), URI: flag.Arg(1)}, log.DefaultLogger)
	if err != nil {
		fmt.Println("Failed to open old database:", err)
		os.Exit(30)
	}
	err = oldDB.Init()
	if err != nil {
		fmt.Println("Failed to upgrade old database:", err)
		os.Exit(31)
	}

	newDB, err := database.New(bridge.Config.AppService.Database, log.DefaultLogger)
	if err != nil {
		fmt.Println("Failed to open new database:", err)
		os.Exit(32)
	}
	err = newDB.Init()
	if err != nil {
		fmt.Println("Failed to upgrade new database:", err)
		os.Exit(33)
	}

	database.Migrate(oldDB, newDB)
}

type Bridge struct {
	AS             *appservice.AppService
	EventProcessor *appservice.EventProcessor
	MatrixHandler  *MatrixHandler
	Config         *config.Config
	DB             *database.Database
	Log            log.Logger
	StateStore     *database.SQLStateStore
	Provisioning   *ProvisioningAPI
	Bot            *appservice.IntentAPI
	Formatter      *Formatter
	Crypto         Crypto
	Metrics        *MetricsHandler
	WAContainer    *sqlstore.Container

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

type Crypto interface {
	HandleMemberEvent(*event.Event)
	Decrypt(*event.Event) (*event.Event, error)
	Encrypt(id.RoomID, event.Type, event.Content) (*event.EncryptedEventContent, error)
	WaitForSession(id.RoomID, id.SenderKey, id.SessionID, time.Duration) bool
	RequestSession(id.RoomID, id.SenderKey, id.SessionID, id.UserID, id.DeviceID)
	ResetSession(id.RoomID)
	Init() error
	Start()
	Stop()
}

func (bridge *Bridge) ensureConnection() {
	for {
		resp, err := bridge.Bot.Whoami()
		if err != nil {
			if errors.Is(err, mautrix.MUnknownToken) {
				bridge.Log.Fatalln("The as_token was not accepted. Is the registration file installed in your homeserver correctly?")
				os.Exit(16)
			} else if errors.Is(err, mautrix.MExclusive) {
				bridge.Log.Fatalln("The as_token was accepted, but the /register request was not. Are the homeserver domain and username template in the config correct, and do they match the values in the registration?")
				os.Exit(16)
			}
			bridge.Log.Errorfln("Failed to connect to homeserver: %v. Retrying in 10 seconds...", err)
			time.Sleep(10 * time.Second)
		} else if resp.UserID != bridge.Bot.UserID {
			bridge.Log.Fatalln("Unexpected user ID in whoami call: got %s, expected %s", resp.UserID, bridge.Bot.UserID)
			os.Exit(17)
		} else {
			break
		}
	}
}

func (bridge *Bridge) Init() {
	var err error

	bridge.AS, err = bridge.Config.MakeAppService()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to initialize AppService:", err)
		os.Exit(11)
	}
	_, _ = bridge.AS.Init()

	bridge.Log = log.Create()
	bridge.Config.Logging.Configure(bridge.Log)
	log.DefaultLogger = bridge.Log.(*log.BasicLogger)
	if len(bridge.Config.Logging.FileNameFormat) > 0 {
		err = log.OpenFile()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Failed to open log file:", err)
			os.Exit(12)
		}
	}
	bridge.AS.Log = log.Sub("Matrix")
	bridge.Bot = bridge.AS.BotIntent()
	bridge.Log.Infoln("Initializing", VersionString)

	bridge.Log.Debugln("Initializing database connection")
	bridge.DB, err = database.New(bridge.Config.AppService.Database, bridge.Log)
	if err != nil {
		bridge.Log.Fatalln("Failed to initialize database connection:", err)
		os.Exit(14)
	}

	bridge.Log.Debugln("Initializing state store")
	bridge.StateStore = database.NewSQLStateStore(bridge.DB)
	bridge.AS.StateStore = bridge.StateStore

	bridge.WAContainer = sqlstore.NewWithDB(bridge.DB.DB, bridge.Config.AppService.Database.Type, nil)

	ss := bridge.Config.AppService.Provisioning.SharedSecret
	if len(ss) > 0 && ss != "disable" {
		bridge.Provisioning = &ProvisioningAPI{bridge: bridge}
	}

	bridge.Log.Debugln("Initializing Matrix event processor")
	bridge.EventProcessor = appservice.NewEventProcessor(bridge.AS)
	bridge.Log.Debugln("Initializing Matrix event handler")
	bridge.MatrixHandler = NewMatrixHandler(bridge)
	bridge.Formatter = NewFormatter(bridge)
	bridge.Crypto = NewCryptoHelper(bridge)
	bridge.Metrics = NewMetricsHandler(bridge.Config.Metrics.Listen, bridge.Log.Sub("Metrics"), bridge.DB)

	store.BaseClientPayload.UserAgent.OsVersion = proto.String(WAVersion)
	store.BaseClientPayload.UserAgent.OsBuildNumber = proto.String(WAVersion)
	store.CompanionProps.Os = proto.String(bridge.Config.WhatsApp.OSName)
	store.CompanionProps.RequireFullSync = proto.Bool(bridge.Config.Bridge.HistorySync.RequestFullSync)
	versionParts := strings.Split(WAVersion, ".")
	if len(versionParts) > 2 {
		primary, _ := strconv.Atoi(versionParts[0])
		secondary, _ := strconv.Atoi(versionParts[1])
		tertiary, _ := strconv.Atoi(versionParts[2])
		store.CompanionProps.Version.Primary = proto.Uint32(uint32(primary))
		store.CompanionProps.Version.Secondary = proto.Uint32(uint32(secondary))
		store.CompanionProps.Version.Tertiary = proto.Uint32(uint32(tertiary))
	}
	platformID, ok := waProto.CompanionProps_CompanionPropsPlatformType_value[strings.ToUpper(bridge.Config.WhatsApp.BrowserName)]
	if ok {
		store.CompanionProps.PlatformType = waProto.CompanionProps_CompanionPropsPlatformType(platformID).Enum()
	}
}

func (bridge *Bridge) Start() {
	bridge.Log.Debugln("Running database upgrades")
	err := bridge.DB.Init()
	if err != nil && (!errors.Is(err, upgrades.ErrUnsupportedDatabaseVersion) || !*ignoreUnsupportedDatabase) {
		bridge.Log.Fatalln("Failed to initialize database:", err)
		if errors.Is(err, upgrades.ErrForeignTables) {
			bridge.Log.Infoln("You can use --ignore-foreign-tables to ignore this error")
		} else if errors.Is(err, upgrades.ErrNotOwned) {
			bridge.Log.Infoln("Sharing the same database with different programs is not supported")
		} else if errors.Is(err, upgrades.ErrUnsupportedDatabaseVersion) {
			bridge.Log.Infoln("Downgrading the bridge is not supported")
		}
		os.Exit(15)
	}
	bridge.Log.Debugln("Checking connection to homeserver")
	bridge.ensureConnection()
	if bridge.Crypto != nil {
		err = bridge.Crypto.Init()
		if err != nil {
			bridge.Log.Fatalln("Error initializing end-to-bridge encryption:", err)
			os.Exit(19)
		}
	}
	if bridge.Provisioning != nil {
		bridge.Log.Debugln("Initializing provisioning API")
		bridge.Provisioning.Init()
	}
	bridge.Log.Debugln("Starting application service HTTP server")
	go bridge.AS.Start()
	bridge.Log.Debugln("Starting event processor")
	go bridge.EventProcessor.Start()
	go bridge.CheckWhatsAppUpdate()
	go bridge.UpdateBotProfile()
	if bridge.Crypto != nil {
		go bridge.Crypto.Start()
	}
	go bridge.StartUsers()
	if bridge.Config.Metrics.Enabled {
		go bridge.Metrics.Start()
	}

	if bridge.Config.Bridge.ResendBridgeInfo {
		go bridge.ResendBridgeInfo()
	}
	go bridge.Loop()
	bridge.AS.Ready = true
}

func (bridge *Bridge) CheckWhatsAppUpdate() {
	bridge.Log.Debugfln("Checking for WhatsApp web update")
	resp, err := whatsmeow.CheckUpdate(http.DefaultClient)
	if err != nil {
		bridge.Log.Warnfln("Failed to check for WhatsApp web update: %v", err)
		return
	}
	if store.GetWAVersion() == resp.ParsedVersion {
		bridge.Log.Debugfln("Bridge is using latest WhatsApp web protocol")
	} else if store.GetWAVersion().LessThan(resp.ParsedVersion) {
		if resp.IsBelowHard || resp.IsBroken {
			bridge.Log.Warnfln("Bridge is using outdated WhatsApp web protocol and probably doesn't work anymore (%s, latest is %s)", store.GetWAVersion(), resp.ParsedVersion)
		} else if resp.IsBelowSoft {
			bridge.Log.Infofln("Bridge is using outdated WhatsApp web protocol (%s, latest is %s)", store.GetWAVersion(), resp.ParsedVersion)
		} else {
			bridge.Log.Debugfln("Bridge is using outdated WhatsApp web protocol (%s, latest is %s)", store.GetWAVersion(), resp.ParsedVersion)
		}
	} else {
		bridge.Log.Debugfln("Bridge is using newer than latest WhatsApp web protocol")
	}
}

func (bridge *Bridge) Loop() {
	for {
		bridge.SleepAndDeleteUpcoming()
		time.Sleep(1 * time.Hour)
		bridge.WarnUsersAboutDisconnection()
	}
}

func (bridge *Bridge) WarnUsersAboutDisconnection() {
	bridge.usersLock.Lock()
	for _, user := range bridge.usersByUsername {
		if user.IsConnected() && !user.PhoneRecentlySeen(true) {
			go user.sendPhoneOfflineWarning()
		}
	}
	bridge.usersLock.Unlock()
}

func (bridge *Bridge) ResendBridgeInfo() {
	if *dontSaveConfig {
		bridge.Log.Warnln("Not setting resend_bridge_info to false in config due to --no-update flag")
	} else {
		err := config.Mutate(*configPath, func(helper *config.UpgradeHelper) {
			helper.Set(config.Bool, "false", "bridge", "resend_bridge_info")
		})
		if err != nil {
			bridge.Log.Errorln("Failed to save config after setting resend_bridge_info to false:", err)
		}
	}
	bridge.Log.Infoln("Re-sending bridge info state event to all portals")
	for _, portal := range bridge.GetAllPortals() {
		portal.UpdateBridgeInfo()
	}
	bridge.Log.Infoln("Finished re-sending bridge info state events")
}

func (bridge *Bridge) UpdateBotProfile() {
	bridge.Log.Debugln("Updating bot profile")
	botConfig := &bridge.Config.AppService.Bot

	var err error
	var mxc id.ContentURI
	if botConfig.Avatar == "remove" {
		err = bridge.Bot.SetAvatarURL(mxc)
	} else if len(botConfig.Avatar) > 0 {
		mxc, err = id.ParseContentURI(botConfig.Avatar)
		if err == nil {
			err = bridge.Bot.SetAvatarURL(mxc)
		}
		botConfig.ParsedAvatar = mxc
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot avatar:", err)
	}

	if botConfig.Displayname == "remove" {
		err = bridge.Bot.SetDisplayName("")
	} else if len(botConfig.Displayname) > 0 {
		err = bridge.Bot.SetDisplayName(botConfig.Displayname)
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot displayname:", err)
	}
}

func (bridge *Bridge) StartUsers() {
	bridge.Log.Debugln("Starting users")
	foundAnySessions := false
	for _, user := range bridge.GetAllUsers() {
		if !user.JID.IsEmpty() {
			foundAnySessions = true
		}
		go user.Connect()
	}
	if !foundAnySessions {
		bridge.sendGlobalBridgeState(BridgeState{StateEvent: StateUnconfigured}.fill(nil))
	}
	bridge.Log.Debugln("Starting custom puppets")
	for _, loopuppet := range bridge.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			puppet.log.Debugln("Starting custom puppet", puppet.CustomMXID)
			err := puppet.StartCustomMXID(true)
			if err != nil {
				puppet.log.Errorln("Failed to start custom puppet:", err)
			}
		}(loopuppet)
	}
}

func (bridge *Bridge) Stop() {
	if bridge.Crypto != nil {
		bridge.Crypto.Stop()
	}
	bridge.AS.Stop()
	bridge.Metrics.Stop()
	bridge.EventProcessor.Stop()
	for _, user := range bridge.usersByUsername {
		if user.Client == nil {
			continue
		}
		bridge.Log.Debugln("Disconnecting", user.MXID)
		user.Client.Disconnect()
		close(user.historySyncs)
	}
}

func (bridge *Bridge) Main() {
	configData, upgraded, err := config.Upgrade(*configPath, !*dontSaveConfig)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error updating config:", err)
		if configData == nil {
			os.Exit(10)
		}
	}

	bridge.Config, err = config.Load(configData, upgraded)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to parse config:", err)
		os.Exit(10)
	}

	if *generateRegistration {
		bridge.GenerateRegistration()
		return
	} else if *migrateFrom {
		bridge.MigrateDatabase()
		return
	}

	bridge.Init()
	bridge.Log.Infoln("Bridge initialization complete, starting...")
	bridge.Start()
	bridge.Log.Infoln("Bridge started!")

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	bridge.Log.Infoln("Interrupt received, stopping...")
	bridge.Stop()
	bridge.Log.Infoln("Bridge stopped.")
	os.Exit(0)
}

func main() {
	flag.SetHelpTitles(
		"mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.",
		"mautrix-whatsapp [-h] [-c <path>] [-r <path>] [-g] [--migrate-db <source type> <source uri>]")
	err := flag.Parse()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		flag.PrintHelp()
		os.Exit(1)
	} else if *wantHelp {
		flag.PrintHelp()
		os.Exit(0)
	} else if *version {
		fmt.Println(VersionString)
		return
	}
	upgrades.IgnoreForeignTables = *ignoreForeignTables

	(&Bridge{
		usersByMXID:         make(map[id.UserID]*User),
		usersByUsername:     make(map[string]*User),
		spaceRooms:          make(map[id.RoomID]*User),
		managementRooms:     make(map[id.RoomID]*User),
		portalsByMXID:       make(map[id.RoomID]*Portal),
		portalsByJID:        make(map[database.PortalKey]*Portal),
		puppets:             make(map[types.JID]*Puppet),
		puppetsByCustomMXID: make(map[id.UserID]*Puppet),
	}).Main()
}
