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

	"maunium.net/go/mautrix"
	appservice "maunium.net/go/mautrix-appservice"
)

func (user *User) inviteToCommunity() {
	url := user.bridge.Bot.BuildURL("groups", user.CommunityID, "admin", "users", "invite", user.MXID)
	reqBody := map[string]interface{}{}
	_, err := user.bridge.Bot.MakeRequest(http.MethodPut, url, &reqBody, nil)
	if err != nil {
		user.log.Warnfln("Failed to invite user to personal filtering community %s: %v", user.CommunityID, err)
	}
}

func (user *User) updateCommunityProfile() {
	url := user.bridge.Bot.BuildURL("groups", user.CommunityID, "profile")
	profileReq := struct {
		Name             string `json:"name"`
		AvatarURL        string `json:"avatar_url"`
		ShortDescription string `json:"short_description"`
	}{"WhatsApp", user.bridge.Config.AppService.Bot.Avatar, "Your WhatsApp bridged chats"}
	_, err := user.bridge.Bot.MakeRequest(http.MethodPost, url, &profileReq, nil)
	if err != nil {
		user.log.Warnfln("Failed to update metadata of %s: %v", user.CommunityID, err)
	}
}

func (user *User) createCommunity() {
	if user.IsRelaybot || !user.bridge.Config.Bridge.EnableCommunities() {
		return
	}

	localpart, server := appservice.ParseUserID(user.MXID)
	community := user.bridge.Config.Bridge.FormatCommunity(localpart, server)
	user.log.Debugln("Creating personal filtering community", community)
	bot := user.bridge.Bot
	req := struct {
		Localpart string `json:"localpart"`
	}{community}
	resp := struct {
		GroupID string `json:"group_id"`
	}{}
	_, err := bot.MakeRequest(http.MethodPost, bot.BuildURL("create_group"), &req, &resp)
	if err != nil {
		if httpErr, ok := err.(mautrix.HTTPError); ok {
			if httpErr.RespError.Err != "Group already exists" {
				user.log.Warnln("Server responded with error creating personal filtering community:", err)
				return
			} else {
				user.log.Debugln("Personal filtering community", resp.GroupID, "already existed")
				user.CommunityID = fmt.Sprintf("+%s:%s", req.Localpart, user.bridge.Config.Homeserver.Domain)
			}
		} else {
			user.log.Warnln("Unknown error creating personal filtering community:", err)
			return
		}
	} else {
		user.log.Infoln("Created personal filtering community %s", resp.GroupID)
		user.CommunityID = resp.GroupID
		user.inviteToCommunity()
		user.updateCommunityProfile()
	}
}

func (user *User) addPuppetToCommunity(puppet *Puppet) bool {
	if user.IsRelaybot || len(user.CommunityID) == 0 {
		return false
	}
	bot := user.bridge.Bot
	url := bot.BuildURL("groups", user.CommunityID, "admin", "users", "invite", puppet.MXID)
	blankReqBody := map[string]interface{}{}
	_, err := bot.MakeRequest(http.MethodPut, url, &blankReqBody, nil)
	if err != nil {
		user.log.Warnfln("Failed to invite %s to %s: %v", puppet.MXID, user.CommunityID, err)
		return false
	}
	reqBody := map[string]map[string]string{
		"m.visibility": {
			"type": "private",
		},
	}
	url = bot.BuildURLWithQuery([]string{"groups", user.CommunityID, "self", "accept_invite"}, map[string]string{
		"user_id": puppet.MXID,
	})
	_, err = bot.MakeRequest(http.MethodPut, url, &reqBody, nil)
	if err != nil {
		user.log.Warnfln("Failed to join %s as %s: %v", user.CommunityID, puppet.MXID, err)
		return false
	}
	user.log.Debugln("Added", puppet.MXID, "to", user.CommunityID)
	return true
}

func (user *User) addPortalToCommunity(portal *Portal) bool {
	if user.IsRelaybot || len(user.CommunityID) == 0 || len(portal.MXID) == 0 {
		return false
	}
	bot := user.bridge.Bot
	url := bot.BuildURL("groups", user.CommunityID, "admin", "rooms", portal.MXID)
	reqBody := map[string]map[string]string{
		"m.visibility": {
			"type": "private",
		},
	}
	_, err := bot.MakeRequest(http.MethodPut, url, &reqBody, nil)
	if err != nil {
		user.log.Warnfln("Failed to add %s to %s: %v", portal.MXID, user.CommunityID, err)
		return false
	}
	user.log.Debugln("Added", portal.MXID, "to", user.CommunityID)
	return true
}
