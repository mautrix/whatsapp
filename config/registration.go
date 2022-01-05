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

package config

import (
	"fmt"
	"regexp"

	"maunium.net/go/mautrix/appservice"
)

func (config *Config) NewRegistration() (*appservice.Registration, error) {
	registration := appservice.CreateRegistration()

	err := config.copyToRegistration(registration)
	if err != nil {
		return nil, err
	}

	config.AppService.ASToken = registration.AppToken
	config.AppService.HSToken = registration.ServerToken

	// Workaround for https://github.com/matrix-org/synapse/pull/5758
	registration.SenderLocalpart = appservice.RandomString(32)
	botRegex := regexp.MustCompile(fmt.Sprintf("^@%s:%s$", config.AppService.Bot.Username, config.Homeserver.Domain))
	registration.Namespaces.RegisterUserIDs(botRegex, true)

	return registration, nil
}

func (config *Config) GetRegistration() (*appservice.Registration, error) {
	registration := appservice.CreateRegistration()

	err := config.copyToRegistration(registration)
	if err != nil {
		return nil, err
	}

	registration.AppToken = config.AppService.ASToken
	registration.ServerToken = config.AppService.HSToken
	return registration, nil
}

func (config *Config) copyToRegistration(registration *appservice.Registration) error {
	registration.ID = config.AppService.ID
	registration.URL = config.AppService.Address
	falseVal := false
	registration.RateLimited = &falseVal
	registration.SenderLocalpart = config.AppService.Bot.Username
	registration.EphemeralEvents = config.AppService.EphemeralEvents

	userIDRegex, err := regexp.Compile(fmt.Sprintf("^@%s:%s$",
		config.Bridge.FormatUsername("[0-9]+"),
		config.Homeserver.Domain))
	if err != nil {
		return err
	}
	registration.Namespaces.RegisterUserIDs(userIDRegex, true)
	return nil
}
