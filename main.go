// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2019 Tulir Asokan
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
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	flag "maunium.net/go/mauflag"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix-appservice"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/database/upgrades"
	"maunium.net/go/mautrix-whatsapp/types"
)

var configPath = flag.MakeFull("c", "config", "The path to your config file.", "config.yaml").String()

//var baseConfigPath = flag.MakeFull("b", "base-config", "The path to the example config file.", "example-config.yaml").String()
var registrationPath = flag.MakeFull("r", "registration", "The path where to save the appservice registration.", "registration.yaml").String()
var generateRegistration = flag.MakeFull("g", "generate-registration", "Generate registration and quit.", "false").Bool()
var ignoreUnsupportedDatabase = flag.Make().LongKey("ignore-unsupported-database").Usage("Run even if database is too new").Default("false").Bool()
var migrateFrom = flag.Make().LongKey("migrate-db").Usage("Source database type and URI to migrate from.").Bool()
var wantHelp, _ = flag.MakeHelpFlag()

func (bridge *Bridge) GenerateRegistration() {
	reg, err := bridge.Config.NewRegistration()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to generate registration:", err)
		os.Exit(20)
	}

	err = reg.Save(*registrationPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to save registration:", err)
		os.Exit(21)
	}

	err = bridge.Config.Save(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to save config:", err)
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
		bridge.Log.Fatalln("Failed to open new database:", err)
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

	usersByMXID         map[types.MatrixUserID]*User
	usersByJID          map[types.WhatsAppID]*User
	usersLock           sync.Mutex
	managementRooms     map[types.MatrixRoomID]*User
	managementRoomsLock sync.Mutex
	portalsByMXID       map[types.MatrixRoomID]*Portal
	portalsByJID        map[database.PortalKey]*Portal
	portalsLock         sync.Mutex
	puppets             map[types.WhatsAppID]*Puppet
	puppetsByCustomMXID map[types.MatrixUserID]*Puppet
	puppetsLock         sync.Mutex
}

func NewBridge() *Bridge {
	bridge := &Bridge{
		usersByMXID:         make(map[types.MatrixUserID]*User),
		usersByJID:          make(map[types.WhatsAppID]*User),
		managementRooms:     make(map[types.MatrixRoomID]*User),
		portalsByMXID:       make(map[types.MatrixRoomID]*Portal),
		portalsByJID:        make(map[database.PortalKey]*Portal),
		puppets:             make(map[types.WhatsAppID]*Puppet),
		puppetsByCustomMXID: make(map[types.MatrixUserID]*Puppet),
	}

	var err error
	bridge.Config, err = config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(10)
	}
	return bridge
}

func (bridge *Bridge) ensureConnection() {
	url := bridge.Bot.BuildURL("account", "whoami")
	resp := struct {
		UserID string `json:"user_id"`
	}{}
	for {
		_, err := bridge.Bot.MakeRequest(http.MethodGet, url, nil, &resp)
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
}

func (bridge *Bridge) Start() {
	err := bridge.DB.Init()
	if err != nil {
		bridge.Log.Fatalln("Failed to initialize database:", err)
		os.Exit(15)
	}
	if bridge.Provisioning != nil {
		bridge.Log.Debugln("Initializing provisioning API")
		bridge.Provisioning.Init()
	}
	bridge.LoadRelaybot()
	bridge.Log.Debugln("Checking connection to homeserver")
	bridge.ensureConnection()
	bridge.Log.Debugln("Starting application service HTTP server")
	go bridge.AS.Start()
	bridge.Log.Debugln("Starting event processor")
	go bridge.EventProcessor.Start()
	go bridge.UpdateBotProfile()
	go bridge.StartUsers()
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
	if botConfig.Avatar == "remove" {
		err = bridge.Bot.SetAvatarURL("")
	} else if len(botConfig.Avatar) > 0 {
		err = bridge.Bot.SetAvatarURL(botConfig.Avatar)
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
	bridge.AS.Stop()
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
		fmt.Fprintln(os.Stderr, err)
		flag.PrintHelp()
		os.Exit(1)
	} else if *wantHelp {
		flag.PrintHelp()
		os.Exit(0)
	}

	NewBridge().Main()
}
