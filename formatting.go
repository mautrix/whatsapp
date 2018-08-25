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

package main

import (
	"fmt"
	"regexp"
	"strings"

	"maunium.net/go/gomatrix/format"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

func (user *User) newHTMLParser() *format.HTMLParser {
	return &format.HTMLParser{
		TabsToSpaces: 4,
		Newline:      "\n",

		PillConverter: func(mxid, eventID string) string {
			if mxid[0] == '@' {
				puppet := user.GetPuppetByMXID(mxid)
				fmt.Println(mxid, puppet)
				if puppet != nil {
					return "@" + puppet.PhoneNumber()
				}
			}
			return mxid
		},
		BoldConverter: func(text string) string {
			return fmt.Sprintf("*%s*", text)
		},
		ItalicConverter: func(text string) string {
			return fmt.Sprintf("_%s_", text)
		},
		StrikethroughConverter: func(text string) string {
			return fmt.Sprintf("~%s~", text)
		},
		MonospaceConverter: func(text string) string {
			return fmt.Sprintf("```%s```", text)
		},
		MonospaceBlockConverter: func(text string) string {
			return fmt.Sprintf("```%s```", text)
		},
	}
}

var italicRegex = regexp.MustCompile("([\\s>~*]|^)_(.+?)_([^a-zA-Z\\d]|$)")
var boldRegex = regexp.MustCompile("([\\s>_~]|^)\\*(.+?)\\*([^a-zA-Z\\d]|$)")
var strikethroughRegex = regexp.MustCompile("([\\s>_*]|^)~(.+?)~([^a-zA-Z\\d]|$)")
var codeBlockRegex = regexp.MustCompile("```(?:.|\n)+?```")
var mentionRegex = regexp.MustCompile("@[0-9]+")

func (user *User) newWhatsAppFormatMaps() (map[*regexp.Regexp]string, map[*regexp.Regexp]func(string) string) {
	return map[*regexp.Regexp]string{
		italicRegex:        "$1<em>$2</em>$3",
		boldRegex:          "$1<strong>$2</strong>$3",
		strikethroughRegex: "$1<del>$2</del>$3",
	}, map[*regexp.Regexp]func(string) string{
		codeBlockRegex: func(str string) string {
			str = str[3 : len(str)-3]
			if strings.ContainsRune(str, '\n') {
				return fmt.Sprintf("<pre><code>%s</code></pre>", str)
			}
			return fmt.Sprintf("<code>%s</code>", str)
		},
		mentionRegex: func(str string) string {
			jid := str[1:] + whatsappExt.NewUserSuffix
			puppet := user.GetPuppetByJID(jid)
			return fmt.Sprintf(`<a href="https://matrix.to/#/%s">%s</a>`, puppet.MXID, puppet.Displayname)
		},
	}
}
