// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"fmt"

	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"
)

var ExampleConfig string

type Config struct {
	Homeserver struct {
		Address                       string `yaml:"address"`
		Domain                        string `yaml:"domain"`
		Asmux                         bool   `yaml:"asmux"`
		StatusEndpoint                string `yaml:"status_endpoint"`
		MessageSendCheckpointEndpoint string `yaml:"message_send_checkpoint_endpoint"`
		AsyncMedia                    bool   `yaml:"async_media"`
	} `yaml:"homeserver"`

	AppService struct {
		Address  string `yaml:"address"`
		Hostname string `yaml:"hostname"`
		Port     uint16 `yaml:"port"`

		Database DatabaseConfig `yaml:"database"`

		Provisioning struct {
			Prefix       string `yaml:"prefix"`
			SharedSecret string `yaml:"shared_secret"`
		} `yaml:"provisioning"`

		ID  string `yaml:"id"`
		Bot struct {
			Username    string `yaml:"username"`
			Displayname string `yaml:"displayname"`
			Avatar      string `yaml:"avatar"`

			ParsedAvatar id.ContentURI `yaml:"-"`
		} `yaml:"bot"`

		EphemeralEvents bool `yaml:"ephemeral_events"`

		ASToken string `yaml:"as_token"`
		HSToken string `yaml:"hs_token"`
	} `yaml:"appservice"`

	SegmentKey string `yaml:"segment_key"`

	Metrics struct {
		Enabled bool   `yaml:"enabled"`
		Listen  string `yaml:"listen"`
	} `yaml:"metrics"`

	WhatsApp struct {
		OSName      string `yaml:"os_name"`
		BrowserName string `yaml:"browser_name"`
	} `yaml:"whatsapp"`

	Bridge BridgeConfig `yaml:"bridge"`

	Logging appservice.LogConfig `yaml:"logging"`
}

func (config *Config) CanAutoDoublePuppet(userID id.UserID) bool {
	_, homeserver, _ := userID.Parse()
	_, hasSecret := config.Bridge.LoginSharedSecretMap[homeserver]
	return hasSecret
}

func (config *Config) CanDoublePuppetBackfill(userID id.UserID) bool {
	if !config.Bridge.HistorySync.DoublePuppetBackfill {
		return false
	}
	_, homeserver, _ := userID.Parse()
	// Batch sending can only use local users, so don't allow double puppets on other servers.
	if homeserver != config.Homeserver.Domain {
		return false
	}
	return true
}

func Load(data []byte, upgraded bool) (*Config, error) {
	var config = &Config{}
	if !upgraded {
		// Fallback: if config upgrading failed, load example config for base values
		err := yaml.Unmarshal([]byte(ExampleConfig), config)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal example config: %w", err)
		}
	}
	err := yaml.Unmarshal(data, config)
	if err != nil {
		return nil, err
	}

	return config, err
}

func (config *Config) MakeAppService() (*appservice.AppService, error) {
	as := appservice.Create()
	as.HomeserverDomain = config.Homeserver.Domain
	as.HomeserverURL = config.Homeserver.Address
	as.Host.Hostname = config.AppService.Hostname
	as.Host.Port = config.AppService.Port
	as.MessageSendCheckpointEndpoint = config.Homeserver.MessageSendCheckpointEndpoint
	as.DefaultHTTPRetries = 4
	var err error
	as.Registration, err = config.GetRegistration()
	return as, err
}

type DatabaseConfig struct {
	Type string `yaml:"type"`
	URI  string `yaml:"uri"`

	MaxOpenConns int `yaml:"max_open_conns"`
	MaxIdleConns int `yaml:"max_idle_conns"`

	ConnMaxIdleTime string `yaml:"conn_max_idle_time"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}
