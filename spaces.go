package main

import (
	"maunium.net/go/mautrix"
)

func (user *User) createSpace() {
	if user.IsRelaybot || !user.bridge.Config.Bridge.EnableSpaces() {
		return
	}

	user.log.Debug("Creating a user space")

	var roomToCreate *mautrix.ReqCreateRoom
	roomToCreate.Topic = "WhatsApp bridge space"
	roomToCreate.IsDirect = true
	roomToCreate.CreationContent = map[string]interface{}{
		"types": "m.space",
	}

	resp, err := user.bridge.Bot.CreateRoom(roomToCreate)

	if err != nil {
		user.log.Error("Error creating the space", err)
	}

	user.log.Debug("Space created", resp.RoomID.String())
}
