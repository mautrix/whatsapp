// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	flag "maunium.net/go/mauflag"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/database/upgrades"
	"maunium.net/go/mautrix-whatsapp/types"
)

var (
	// These are static
	Name = "mautrix-whatsapp"
	URL  = "https://github.com/tulir/mautrix-whatsapp"
	// This is changed when making a release
	Version   = "0.1.4"
	// This is filled by init()
	WAVersion = ""
	// These are filled at build time with the -X linker flag
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func init() {
	if len(Tag) > 0 && Tag[0] == 'v' {
		Tag = Tag[1:]
	}
	if Tag != Version && !strings.HasSuffix(Version, "+dev") {
		Version += "+dev"
	}
	WAVersion = strings.FieldsFunc(Version, func(r rune) bool { return r == '-' || r == '+' })[0]
}

var configPath = flag.MakeFull("c", "config", "The path to your config file.", "config.yaml").String()

//var baseConfigPath = flag.MakeFull("b", "base-config", "The path to the example config file.", "example-config.yaml").String()
var registrationPath = flag.MakeFull("r", "registration", "The path where to save the appservice registration.", "registration.yaml").String()
var generateRegistration = flag.MakeFull("g", "generate-registration", "Generate registration and quit.", "false").Bool()
var version = flag.MakeFull("v", "version", "View bridge version and quit.", "false").Bool()
var ignoreUnsupportedDatabase = flag.Make().LongKey("ignore-unsupported-database").Usage("Run even if database is too new").Default("false").Bool()
var migrateFrom = flag.Make().LongKey("migrate-db").Usage("Source database type and URI to migrate from.").Bool()
var wantHelp, _ = flag.MakeHelpFlag()

func (bridge *Bridge) GenerateRegistration() {
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

	err = bridge.Config.Save(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to save config:", err)
		os.Exit(22)
	}
	fmt.Println("Registration generated. Add the path to the registration to your Synapse config, restart it, then start the bridge.")
	os.Exit(0)
}

func (bridge *Bridge) MigrateDatabase() {
	oldDB, err := database.New(flag.Arg(0), flag.Arg(1))
	if err != nil {
		fmt.Println("Failed to open old database:", err)
		os.Exit(30)
	}
	err = oldDB.Init()
	if err != nil {
		fmt.Println("Failed to upgrade old database:", err)
		os.Exit(31)
	}

	newDB, err := database.New(bridge.Config.AppService.Database.Type, bridge.Config.AppService.Database.URI)
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
	Relaybot       *User
	Crypto         Crypto
	Metrics *MetricsHandler

	usersByMXID         map[id.UserID]*User
	usersByJID          map[types.WhatsAppID]*User
	usersLock           sync.Mutex
	managementRooms     map[id.RoomID]*User
	managementRoomsLock sync.Mutex
	portalsByMXID       map[id.RoomID]*Portal
	portalsByJID        map[database.PortalKey]*Portal
	portalsLock         sync.Mutex
	puppets             map[types.WhatsAppID]*Puppet
	puppetsByCustomMXID map[id.UserID]*Puppet
	puppetsLock         sync.Mutex
}

type Crypto interface {
	HandleMemberEvent(*event.Event)
	Decrypt(*event.Event) (*event.Event, error)
	Encrypt(id.RoomID, event.Type, event.Content) (*event.EncryptedEventContent, error)
	Init() error
	Start()
	Stop()
}

func NewBridge() *Bridge {
	bridge := &Bridge{
		usersByMXID:         make(map[id.UserID]*User),
		usersByJID:          make(map[types.WhatsAppID]*User),
		managementRooms:     make(map[id.RoomID]*User),
		portalsByMXID:       make(map[id.RoomID]*Portal),
		portalsByJID:        make(map[database.PortalKey]*Portal),
		puppets:             make(map[types.WhatsAppID]*Puppet),
		puppetsByCustomMXID: make(map[id.UserID]*Puppet),
	}

	var err error
	bridge.Config, err = config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(10)
	}
	return bridge
}

func (bridge *Bridge) ensureConnection() {
	for {
		resp, err := bridge.Bot.Whoami()
		if err != nil {
			if httpErr, ok := err.(mautrix.HTTPError); ok && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_UNKNOWN_ACCESS_TOKEN" {
				bridge.Log.Fatalln("Access token invalid. Is the registration installed in your homeserver correctly?")
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
	bridge.Bot = bridge.AS.BotIntent()

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

	bridge.Log.Debugln("Initializing database")
	bridge.DB, err = database.New(bridge.Config.AppService.Database.Type, bridge.Config.AppService.Database.URI)
	if err != nil && (err != upgrades.UnsupportedDatabaseVersion || !*ignoreUnsupportedDatabase) {
		bridge.Log.Fatalln("Failed to initialize database:", err)
		os.Exit(14)
	}

	if len(bridge.Config.AppService.StateStore) > 0 && bridge.Config.AppService.StateStore != "./mx-state.json" {
		version, err := upgrades.GetVersion(bridge.DB.DB)
		if version < 0 && err == nil {
			bridge.Log.Fatalln("Non-standard state store path. Please move the state store to ./mx-state.json " +
				"and update the config. The state store will be migrated into the db on the next launch.")
			os.Exit(18)
		}
	}

	bridge.Log.Debugln("Initializing state store")
	bridge.StateStore = database.NewSQLStateStore(bridge.DB)
	bridge.AS.StateStore = bridge.StateStore

	bridge.DB.SetMaxOpenConns(bridge.Config.AppService.Database.MaxOpenConns)
	bridge.DB.SetMaxIdleConns(bridge.Config.AppService.Database.MaxIdleConns)

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
}

func (bridge *Bridge) Start() {
	err := bridge.DB.Init()
	if err != nil {
		bridge.Log.Fatalln("Failed to initialize database:", err)
		os.Exit(15)
	}
	bridge.Log.Debugln("Checking connection to homeserver")
	bridge.ensureConnection()
	if bridge.Crypto != nil {
		err := bridge.Crypto.Init()
		if err != nil {
			bridge.Log.Fatalln("Error initializing end-to-bridge encryption:", err)
			os.Exit(19)
		}
	}
	if bridge.Provisioning != nil {
		bridge.Log.Debugln("Initializing provisioning API")
		bridge.Provisioning.Init()
	}
	bridge.LoadRelaybot()
	bridge.Log.Debugln("Starting application service HTTP server")
	go bridge.AS.Start()
	bridge.Log.Debugln("Starting event processor")
	go bridge.EventProcessor.Start()
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
}

func (bridge *Bridge) ResendBridgeInfo() {
	bridge.Config.Bridge.ResendBridgeInfo = false
	err := bridge.Config.Save(*configPath)
	if err != nil {
		bridge.Log.Errorln("Failed to save config after setting resend_bridge_info to false:", err)
	}
	bridge.Log.Infoln("Re-sending bridge info state event to all portals")
	for _, portal := range bridge.GetAllPortals() {
		portal.UpdateBridgeInfo()
	}
	bridge.Log.Infoln("Finished re-sending bridge info state events")
}

func (bridge *Bridge) LoadRelaybot() {
	if !bridge.Config.Bridge.Relaybot.Enabled {
		return
	}
	bridge.Relaybot = bridge.GetUserByMXID("relaybot")
	if bridge.Relaybot.HasSession() {
		bridge.Log.Debugln("Relaybot is enabled")
	} else {
		bridge.Log.Debugln("Relaybot is enabled, but not logged in")
	}
	bridge.Relaybot.ManagementRoom = bridge.Config.Bridge.Relaybot.ManagementRoom
	bridge.Relaybot.IsRelaybot = true
	bridge.Relaybot.Connect(false)
}

func (bridge *Bridge) UpdateBotProfile() {
	bridge.Log.Debugln("Updating bot profile")
	botConfig := bridge.Config.AppService.Bot

	var err error
	var mxc id.ContentURI
	if botConfig.Avatar == "remove" {
		err = bridge.Bot.SetAvatarURL(mxc)
	} else if len(botConfig.Avatar) > 0 {
		mxc, err = id.ParseContentURI(botConfig.Avatar)
		if err == nil {
			err = bridge.Bot.SetAvatarURL(mxc)
		}
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot avatar:", err)
	}

	if botConfig.Displayname == "remove" {
		err = bridge.Bot.SetDisplayName("")
	} else if len(botConfig.Avatar) > 0 {
		err = bridge.Bot.SetDisplayName(botConfig.Displayname)
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot displayname:", err)
	}
}

func (bridge *Bridge) StartUsers() {
	bridge.Log.Debugln("Starting users")
	for _, user := range bridge.GetAllUsers() {
		go user.Connect(false)
	}
	bridge.Log.Debugln("Starting custom puppets")
	for _, loopuppet := range bridge.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			puppet.log.Debugln("Starting custom puppet", puppet.CustomMXID)
			err := puppet.StartCustomMXID()
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
	for _, user := range bridge.usersByJID {
		if user.Conn == nil {
			continue
		}
		bridge.Log.Debugln("Disconnecting", user.MXID)
		sess, err := user.Conn.Disconnect()
		if err != nil {
			bridge.Log.Errorfln("Error while disconnecting %s: %v", user.MXID, err)
		} else if len(sess.Wid) > 0 {
			user.SetSession(&sess)
		}
	}
}

func (bridge *Bridge) Main() {
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
		if Tag == Version {
			fmt.Printf("%s %s (%s)\n", Name, Tag, BuildTime)
		} else if len(Commit) > 8 {
			fmt.Printf("%s %s.%s (%s)\n", Name, Version, Commit[:8], BuildTime)
		} else {
			fmt.Printf("%s %s.unknown\n", Name, Version)
		}
		return
	}

	NewBridge().Main()
}
