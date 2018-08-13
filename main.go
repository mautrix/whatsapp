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
	"github.com/Rhymen/go-whatsapp"
	"time"
	"fmt"
	"os"
	"bufio"
	"encoding/gob"
	"github.com/mdp/qrterminal"
	"maunium.net/go/mautrix-whatsapp/config"
	flag "maunium.net/go/mauflag"
	"os/signal"
	"syscall"
	"maunium.net/go/mautrix-appservice"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/database"
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
	AppService *appservice.AppService
	Config     *config.Config
	DB         *database.Database
	Log        *log.Logger

	MatrixListener *MatrixListener
}

func NewBridge() *Bridge {
	bridge := &Bridge{}
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
	bridge.Log = bridge.AppService.Log.Parent
	log.DefaultLogger = bridge.Log
	bridge.AppService.Log = log.CreateSublogger("Matrix", log.LevelDebug)

	bridge.DB, err = database.New(bridge.Config.AppService.Database.URI)
	if err != nil {
		bridge.Log.Fatalln("Failed to initialize database:", err)
		os.Exit(12)
	}

	bridge.MatrixListener = NewMatrixListener(bridge)
}

func (bridge *Bridge) Start() {
	bridge.AppService.Start()
	bridge.MatrixListener.Start()
}

func (bridge *Bridge) Stop() {
	bridge.AppService.Stop()
	bridge.MatrixListener.Stop()
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
	flag.SetHelpTitles("mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.", "[-h] [-c <path>] [-r <path>] [-g]")
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

func temp() {
	wac, err := whatsapp.NewConn(20 * time.Second)
	if err != nil {
		panic(err)
	}

	wac.AddHandler(myHandler{})

	sess, err := LoadSession("whatsapp.session")
	if err != nil {
		fmt.Println(err)
		sess, err = Login(wac)
	} else {
		sess, err = wac.RestoreSession(sess)
	}
	if err != nil {
		panic(err)
	}
	SaveSession(sess, "whatsapp.session")

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("receiver> ")
		receiver, _ := reader.ReadString('\n')
		fmt.Print("message> ")
		message, _ := reader.ReadString('\n')
		wac.Send(whatsapp.TextMessage{
			Info: whatsapp.MessageInfo{
				RemoteJid: fmt.Sprintf("%s@s.whatsapp.net", receiver),
			},
			Text: message,
		})
		fmt.Println(receiver, message)
	}
}

func Login(wac *whatsapp.Conn) (whatsapp.Session, error) {
	qrChan := make(chan string)
	go func() {
		qrterminal.Generate(<-qrChan, qrterminal.L, os.Stdout)
	}()
	return wac.Login(qrChan)
}

func SaveSession(session whatsapp.Session, fileName string) {
	file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		panic(err)
	}

	enc := gob.NewEncoder(file)
	enc.Encode(session)
}

func LoadSession(fileName string) (sess whatsapp.Session, err error) {
	file, err := os.OpenFile(fileName, os.O_RDONLY, 0600)
	if err != nil {
		return sess, err
	}

	dec := gob.NewDecoder(file)
	dec.Decode(sess)
	return
}

type myHandler struct{}

func (myHandler) HandleError(err error) {
	fmt.Fprintf(os.Stderr, "%v", err)
}

func (myHandler) HandleTextMessage(message whatsapp.TextMessage) {
	fmt.Println(message)
}

func (myHandler) HandleImageMessage(message whatsapp.ImageMessage) {
	fmt.Println(message)
}

func (myHandler) HandleVideoMessage(message whatsapp.VideoMessage) {
	fmt.Println(message)
}

func (myHandler) HandleJsonMessage(message string) {
	fmt.Println(message)
}
