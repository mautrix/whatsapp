// mautrix-whatsapp - A Matrix-Whatsapp puppeting bridge.
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
)

func main() {
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
