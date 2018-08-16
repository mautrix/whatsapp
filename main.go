// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
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
	"maunium.net/go/mautrix-whatsapp/config"
	flag "maunium.net/go/mauflag"
	"os/signal"
	"syscall"
	"maunium.net/go/mautrix-appservice"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/gomatrix"
	"maunium.net/go/mautrix-whatsapp/types"
)

var configPath = flag.MakeFull("c", "config", "The path to your config file.", "config.yaml").String()
var registrationPath = flag.MakeFull("r", "registration", "The path where to save the appservice registration.", "registration.yaml").String()
var generateRegistration = flag.MakeFull("g", "generate-registration", "Generate registration and quit.", "false").Bool()
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
	fmt.Println("Registration generated. Add the path to the registration to your Synapse config restart it, then start the bridge.")
	os.Exit(0)
}

type Bridge struct {
	AppService     *appservice.AppService
	EventProcessor *appservice.EventProcessor
	Config         *config.Config
	DB             *database.Database
	Log            log.Logger

	StateStore *AutosavingStateStore

	users map[types.MatrixUserID]*User
}

func NewBridge() *Bridge {
	bridge := &Bridge{
		users: make(map[types.MatrixUserID]*User),
	}
	var err error
	bridge.Config, err = config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load config:", err)
		os.Exit(10)
	}
	return bridge
}

func (bridge *Bridge) Init() {
	var err error

	bridge.AppService, err = bridge.Config.MakeAppService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize AppService:", err)
		os.Exit(11)
	}
	bridge.AppService.Init()
	bridge.Log = bridge.AppService.Log
	log.DefaultLogger = bridge.Log.(*log.BasicLogger)
	bridge.AppService.Log = log.Sub("Matrix")

	bridge.Log.Debugln("Initializing state store")
	bridge.StateStore = NewAutosavingStateStore(bridge.Config.AppService.StateStore)
	err = bridge.StateStore.Load()
	if err != nil {
		bridge.Log.Fatalln("Failed to load state store:", err)
		os.Exit(12)
	}
	bridge.AppService.StateStore = bridge.StateStore

	bridge.Log.Debugln("Initializing database")
	bridge.DB, err = database.New(bridge.Config.AppService.Database.URI)
	if err != nil {
		bridge.Log.Fatalln("Failed to initialize database:", err)
		os.Exit(13)
	}

	bridge.Log.Debugln("Initializing event processor")
	bridge.EventProcessor = appservice.NewEventProcessor(bridge.AppService)
	bridge.EventProcessor.On(gomatrix.EventMessage, bridge.HandleMessage)
	bridge.EventProcessor.On(gomatrix.StateMember, bridge.HandleMembership)
}

func (bridge *Bridge) Start() {
	bridge.DB.CreateTables()
	bridge.Log.Debugln("Starting application service HTTP server")
	go bridge.AppService.Start()
	bridge.Log.Debugln("Starting event processor")
	go bridge.EventProcessor.Start()
	bridge.Log.Debugln("Updating bot profile")
	go bridge.UpdateBotProfile()
}

func (bridge *Bridge) UpdateBotProfile() {
	botConfig := bridge.Config.AppService.Bot

	var err error
	if botConfig.Avatar == "remove" {
		err = bridge.AppService.BotIntent().SetAvatarURL("")
	} else if len(botConfig.Avatar) > 0 {
		err = bridge.AppService.BotIntent().SetAvatarURL(botConfig.Avatar)
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot avatar:", err)
	}

	if botConfig.Displayname == "remove" {
		err = bridge.AppService.BotIntent().SetDisplayName("")
	} else if len(botConfig.Avatar) > 0 {
		err = bridge.AppService.BotIntent().SetDisplayName(botConfig.Displayname)
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot displayname:", err)
	}
}

func (bridge *Bridge) Stop() {
	bridge.AppService.Stop()
	bridge.EventProcessor.Stop()
	err := bridge.StateStore.Save()
	if err != nil {
		bridge.Log.Warnln("Failed to save state store:", err)
	}
}

func (bridge *Bridge) Main() {
	if *generateRegistration {
		bridge.GenerateRegistration()
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
		"mautrix-whatsapp [-h] [-c <path>] [-r <path>] [-g]")
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
