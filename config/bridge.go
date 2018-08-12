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
	"bytes"
	"text/template"
)

type BridgeConfig struct {
	RawUsernameTemplate    string             `yaml:"username_template"`
	RawDisplaynameTemplate string             `yaml:"displayname_template"`
	UsernameTemplate       *template.Template `yaml:"-"`
	DisplaynameTemplate    *template.Template `yaml:"-"`
}

func (bc BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal(bc)
	if err != nil {
		return err
	}

	bc.UsernameTemplate, err = template.New("username").Parse(bc.RawUsernameTemplate)
	if err != nil {
		return err
	}

	bc.DisplaynameTemplate, err = template.New("displayname").Parse(bc.RawDisplaynameTemplate)
	return err
}

type DisplaynameTemplateArgs struct {
	Displayname string
}

type UsernameTemplateArgs struct {
	Receiver string
	UserID   string
}

func (bc BridgeConfig) FormatDisplayname(displayname string) string {
	var buf bytes.Buffer
	bc.DisplaynameTemplate.Execute(&buf, DisplaynameTemplateArgs{
		Displayname: displayname,
	})
	return buf.String()
}

func (bc BridgeConfig) FormatUsername(receiver, userID string) string {
	var buf bytes.Buffer
	bc.UsernameTemplate.Execute(&buf, UsernameTemplateArgs{
		Receiver: receiver,
		UserID:   userID,
	})
	return buf.String()
}

func (bc BridgeConfig) MarshalYAML() (interface{}, error) {
	bc.RawDisplaynameTemplate = bc.FormatDisplayname("{{.Displayname}}")
	bc.RawUsernameTemplate = bc.FormatUsername("{{.Receiver}}", "{{.UserID}}")
	return bc, nil
}
