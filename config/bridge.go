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
	"bytes"
	"strconv"
	"strings"
	"text/template"

	"github.com/Rhymen/go-whatsapp"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix-whatsapp/types"
)

type BridgeConfig struct {
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`

	CommandPrefix string `yaml:"command_prefix"`

	Permissions PermissionConfig `yaml:"permissions"`

	usernameTemplate    *template.Template `yaml:"-"`
	displaynameTemplate *template.Template `yaml:"-"`
}

type umBridgeConfig BridgeConfig

func (bc *BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umBridgeConfig)(bc))
	if err != nil {
		return err
	}

	bc.usernameTemplate, err = template.New("username").Parse(bc.UsernameTemplate)
	if err != nil {
		return err
	}

	bc.displaynameTemplate, err = template.New("displayname").Parse(bc.DisplaynameTemplate)
	return err
}

type DisplaynameTemplateArgs struct {
	Displayname string
}

type UsernameTemplateArgs struct {
	Receiver string
	UserID   string
}

func (bc BridgeConfig) FormatDisplayname(contact whatsapp.Contact) string {
	var buf bytes.Buffer
	if index := strings.IndexRune(contact.Jid, '@'); index > 0 {
		contact.Jid = "+" + contact.Jid[:index]
	}
	bc.displaynameTemplate.Execute(&buf, contact)
	return buf.String()
}

func (bc BridgeConfig) FormatUsername(receiver types.MatrixUserID, userID types.WhatsAppID) string {
	var buf bytes.Buffer
	receiver = strings.Replace(receiver, "@", "=40", 1)
	receiver = strings.Replace(receiver, ":", "=3", 1)
	bc.usernameTemplate.Execute(&buf, UsernameTemplateArgs{
		Receiver: receiver,
		UserID:   userID,
	})
	return buf.String()
}

func (bc BridgeConfig) MarshalYAML() (interface{}, error) {
	bc.DisplaynameTemplate = bc.FormatDisplayname(whatsapp.Contact{
		Jid:    "{{.Jid}}",
		Notify: "{{.Notify}}",
		Name:   "{{.Name}}",
		Short:  "{{.Short}}",
	})
	bc.UsernameTemplate = bc.FormatUsername("{{.Receiver}}", "{{.UserID}}")
	return bc, nil
}

type PermissionConfig map[string]PermissionLevel

type PermissionLevel int

const (
	PermissionLevelDefault PermissionLevel = 0
	PermissionLevelUser    PermissionLevel = 10
	PermissionLevelAdmin   PermissionLevel = 100
)

func (pc *PermissionConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	rawPC := make(map[string]string)
	err := unmarshal(&rawPC)
	if err != nil {
		return err
	}

	if *pc == nil {
		*pc = make(map[string]PermissionLevel)
	}
	for key, value := range rawPC {
		switch strings.ToLower(value) {
		case "user":
			(*pc)[key] = PermissionLevelUser
		case "admin":
			(*pc)[key] = PermissionLevelAdmin
		default:
			val, err := strconv.Atoi(value)
			if err != nil {
				(*pc)[key] = PermissionLevelDefault
			} else {
				(*pc)[key] = PermissionLevel(val)
			}
		}
	}
	return nil
}

func (pc *PermissionConfig) MarshalYAML() (interface{}, error) {
	if *pc == nil {
		return nil, nil
	}
	rawPC := make(map[string]string)
	for key, value := range *pc {
		switch value {
		case PermissionLevelUser:
			rawPC[key] = "user"
		case PermissionLevelAdmin:
			rawPC[key] = "admin"
		default:
			rawPC[key] = strconv.Itoa(int(value))
		}
	}
	return rawPC, nil
}

func (pc PermissionConfig) IsWhitelisted(userID string) bool {
	return pc.GetPermissionLevel(userID) >= 10
}

func (pc PermissionConfig) IsAdmin(userID string) bool {
	return pc.GetPermissionLevel(userID) >= 100
}

func (pc PermissionConfig) GetPermissionLevel(userID string) PermissionLevel {
	permissions, ok := pc[userID]
	if ok {
		return permissions
	}

	_, homeserver := appservice.ParseUserID(userID)
	permissions, ok = pc[homeserver]
	if len(homeserver) > 0 && ok {
		return permissions
	}

	permissions, ok = pc["*"]
	if ok {
		return permissions
	}

	return PermissionLevelDefault
}
