// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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
	"fmt"

	"maunium.net/go/mautrix/id"
)

func (puppet *Puppet) SwitchCustomMXID(accessToken string, mxid id.UserID) error {
	puppet.CustomMXID = mxid
	puppet.AccessToken = accessToken
	puppet.EnablePresence = puppet.bridge.Config.Bridge.DefaultBridgePresence
	err := puppet.Update(context.TODO())
	if err != nil {
		return fmt.Errorf("failed to save access token: %w", err)
	}
	err = puppet.StartCustomMXID(false)
	if err != nil {
		return err
	}
	// TODO leave rooms with default puppet
	return nil
}

func (puppet *Puppet) ClearCustomMXID() {
	save := puppet.CustomMXID != "" || puppet.AccessToken != ""
	puppet.bridge.puppetsLock.Lock()
	if puppet.CustomMXID != "" && puppet.bridge.puppetsByCustomMXID[puppet.CustomMXID] == puppet {
		delete(puppet.bridge.puppetsByCustomMXID, puppet.CustomMXID)
	}
	puppet.bridge.puppetsLock.Unlock()
	puppet.CustomMXID = ""
	puppet.AccessToken = ""
	puppet.customIntent = nil
	puppet.customUser = nil
	if save {
		err := puppet.Update(context.TODO())
		if err != nil {
			puppet.zlog.Err(err).Msg("Failed to clear custom MXID")
		}
	}
}

func (puppet *Puppet) StartCustomMXID(reloginOnFail bool) error {
	newIntent, newAccessToken, err := puppet.bridge.DoublePuppet.Setup(context.TODO(), puppet.CustomMXID, puppet.AccessToken, reloginOnFail)
	if err != nil {
		puppet.ClearCustomMXID()
		return err
	}
	puppet.bridge.puppetsLock.Lock()
	puppet.bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	puppet.bridge.puppetsLock.Unlock()
	if puppet.AccessToken != newAccessToken {
		puppet.AccessToken = newAccessToken
		err = puppet.Update(context.TODO())
	}
	puppet.customIntent = newIntent
	puppet.customUser = puppet.bridge.GetUserByMXID(puppet.CustomMXID)
	return err
}

func (user *User) tryAutomaticDoublePuppeting() {
	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
		return
	}
	user.zlog.Debug().Msg("Checking if double puppeting needs to be enabled")
	puppet := user.bridge.GetPuppetByJID(user.JID)
	if len(puppet.CustomMXID) > 0 {
		user.zlog.Debug().Msg("User already has double-puppeting enabled")
		// Custom puppet already enabled
		return
	}
	puppet.CustomMXID = user.MXID
	puppet.EnablePresence = puppet.bridge.Config.Bridge.DefaultBridgePresence
	err := puppet.StartCustomMXID(true)
	if err != nil {
		user.zlog.Warn().Err(err).Msg("Failed to login with shared secret")
	} else {
		// TODO leave rooms with default puppet
		user.zlog.Debug().Msg("Successfully automatically enabled custom puppet")
	}
}
