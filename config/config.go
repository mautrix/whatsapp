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

package config

import (
	"maunium.net/go/mautrix-appservice"
	"io/ioutil"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Homeserver struct {
		Address string `yaml:"address"`
		Domain  string `yaml:"domain"`
	} `yaml:"homeserver"`

	AppService struct {
		Address  string `yaml:"address"`
		Hostname string `yaml:"hostname"`
		Port     uint16 `yaml:"port"`

		Database string `yaml:"database"`

		ID  string `yaml:"id"`
		Bot struct {
			Username    string `yaml:"username"`
			Displayname string `yaml:"displayname"`
			Avatar      string `yaml:"avatar"`
		} `yaml:"bot"`

		ASToken string `yaml:"as_token"`
		HSToken string `yaml:"hs_token"`
	} `yaml:"appservice"`

	Bridge BridgeConfig `yaml:"bridge"`

	Logging appservice.LogConfig `yaml:"logging"`
}

func Load(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config = &Config{}
	err = yaml.Unmarshal(data, config)
	return config, err
}

func (config *Config) Save(path string) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, data, 0600)
}

func (config *Config) Appservice() (*appservice.Config, error) {
	as := appservice.Create()
	as.LogConfig = config.Logging
	as.HomeserverDomain = config.Homeserver.Domain
	as.HomeserverURL = config.Homeserver.Address
	var err error
	as.Registration, err = config.GetRegistration()
	return as, err
}
