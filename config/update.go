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

package config

import (
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

func Update(path, basePath string) error {
	oldCfgData, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	oldCfg := make(RecursiveMap)
	err = yaml.Unmarshal(oldCfgData, &oldCfg)
	if err != nil {
		return err
	}

	baseCfgData, err := ioutil.ReadFile(basePath)
	if err != nil {
		return err
	}

	baseCfg := make(RecursiveMap)
	err = yaml.Unmarshal(baseCfgData, &baseCfg)
	if err != nil {
		return err
	}

	err = runUpdate(oldCfg, baseCfg)
	if err != nil {
		return err
	}

	newCfgData, err := yaml.Marshal(&baseCfg)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(path, newCfgData, 0600)
}

func runUpdate(oldCfg, newCfg RecursiveMap) error {
	cp := func(path string) {
		newCfg.CopyFrom(oldCfg, path)
	}

	cp("homeserver.address")
	cp("homeserver.domain")

	cp("appservice.address")
	cp("appservice.hostname")
	cp("appservice.port")

	cp("appservice.database.type")
	cp("appservice.database.uri")
	cp("appservice.state_store_path")

	cp("appservice.id")
	cp("appservice.bot.username")
	cp("appservice.bot.displayname")
	cp("appservice.bot.avatar")

	cp("appservice.bot.as_token")
	cp("appservice.bot.hs_token")

	cp("bridge.username_template")
	cp("bridge.displayname_template")

	cp("bridge.command_prefix")

	cp("bridge.permissions")

	cp("logging.directory")
	cp("logging.file_name_format")
	cp("logging.file_date_format")
	cp("logging.file_mode")
	cp("logging.timestamp_format")
	cp("logging.print_level")

	return nil
}
