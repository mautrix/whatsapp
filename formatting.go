// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2023 Tulir Asokan
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
	"html"
	"regexp"
	"sort"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

var italicRegex = regexp.MustCompile("([\\s>~*]|^)_(.+?)_([^a-zA-Z\\d]|$)")
var boldRegex = regexp.MustCompile("([\\s>_~]|^)\\*(.+?)\\*([^a-zA-Z\\d]|$)")
var strikethroughRegex = regexp.MustCompile("([\\s>_*]|^)~(.+?)~([^a-zA-Z\\d]|$)")
var codeBlockRegex = regexp.MustCompile("```(?:.|\n)+?```")
var inlineURLRegex = regexp.MustCompile(`\[(.+?)]\((.+?)\)`)

const mentionedJIDsContextKey = "fi.mau.whatsapp.mentioned_jids"
const allowedMentionsContextKey = "fi.mau.whatsapp.allowed_mentions"

type Formatter struct {
	bridge *WABridge

	matrixHTMLParser *format.HTMLParser

	waReplString   map[*regexp.Regexp]string
	waReplFunc     map[*regexp.Regexp]func(string) string
	waReplFuncText map[*regexp.Regexp]func(string) string
}

func NewFormatter(bridge *WABridge) *Formatter {
	formatter := &Formatter{
		bridge: bridge,
		matrixHTMLParser: &format.HTMLParser{
			TabsToSpaces: 4,
			Newline:      "\n",

			PillConverter: func(displayname, mxid, eventID string, ctx format.Context) string {
				allowedMentions, _ := ctx.ReturnData[allowedMentionsContextKey].(map[types.JID]bool)
				if mxid[0] == '@' {
					var jid types.JID
					if puppet := bridge.GetPuppetByMXID(id.UserID(mxid)); puppet != nil {
						jid = puppet.JID
					} else if user := bridge.GetUserByMXIDIfExists(id.UserID(mxid)); user != nil {
						jid = user.JID.ToNonAD()
					}
					if !jid.IsEmpty() && (allowedMentions == nil || allowedMentions[jid]) {
						if allowedMentions == nil {
							jids, ok := ctx.ReturnData[mentionedJIDsContextKey].([]string)
							if !ok {
								ctx.ReturnData[mentionedJIDsContextKey] = []string{jid.String()}
							} else {
								ctx.ReturnData[mentionedJIDsContextKey] = append(jids, jid.String())
							}
						}
						return "@" + jid.User
					}
				}
				return displayname
			},
			BoldConverter:           func(text string, _ format.Context) string { return fmt.Sprintf("*%s*", text) },
			ItalicConverter:         func(text string, _ format.Context) string { return fmt.Sprintf("_%s_", text) },
			StrikethroughConverter:  func(text string, _ format.Context) string { return fmt.Sprintf("~%s~", text) },
			MonospaceConverter:      func(text string, _ format.Context) string { return fmt.Sprintf("```%s```", text) },
			MonospaceBlockConverter: func(text, language string, _ format.Context) string { return fmt.Sprintf("```%s```", text) },
		},
		waReplString: map[*regexp.Regexp]string{
			italicRegex:        "$1<em>$2</em>$3",
			boldRegex:          "$1<strong>$2</strong>$3",
			strikethroughRegex: "$1<del>$2</del>$3",
		},
	}
	formatter.waReplFunc = map[*regexp.Regexp]func(string) string{
		codeBlockRegex: func(str string) string {
			str = str[3 : len(str)-3]
			if strings.ContainsRune(str, '\n') {
				return fmt.Sprintf("<pre><code>%s</code></pre>", str)
			}
			return fmt.Sprintf("<code>%s</code>", str)
		},
	}
	formatter.waReplFuncText = map[*regexp.Regexp]func(string) string{}
	return formatter
}

func (formatter *Formatter) getMatrixInfoByJID(roomID id.RoomID, jid types.JID) (mxid id.UserID, displayname string) {
	if puppet := formatter.bridge.GetPuppetByJID(jid); puppet != nil {
		mxid = puppet.MXID
		displayname = puppet.Displayname
	}
	if user := formatter.bridge.GetUserByJID(jid); user != nil {
		mxid = user.MXID
		member := formatter.bridge.StateStore.GetMember(roomID, user.MXID)
		if len(member.Displayname) > 0 {
			displayname = member.Displayname
		}
	}
	return
}

func (formatter *Formatter) ParseWhatsApp(roomID id.RoomID, content *event.MessageEventContent, mentionedJIDs []string, allowInlineURL, forceHTML bool) {
	output := html.EscapeString(content.Body)
	for regex, replacement := range formatter.waReplString {
		output = regex.ReplaceAllString(output, replacement)
	}
	for regex, replacer := range formatter.waReplFunc {
		output = regex.ReplaceAllStringFunc(output, replacer)
	}
	if allowInlineURL {
		output = inlineURLRegex.ReplaceAllStringFunc(output, func(s string) string {
			groups := inlineURLRegex.FindStringSubmatch(s)
			return fmt.Sprintf(`<a href="%s">%s</a>`, groups[2], groups[1])
		})
	}
	alreadyMentioned := make(map[id.UserID]struct{})
	content.Mentions = &event.Mentions{}
	for _, rawJID := range mentionedJIDs {
		jid, err := types.ParseJID(rawJID)
		if err != nil {
			continue
		} else if jid.Server == types.LegacyUserServer {
			jid.Server = types.DefaultUserServer
		}
		mxid, displayname := formatter.getMatrixInfoByJID(roomID, jid)
		number := "@" + jid.User
		output = strings.ReplaceAll(output, number, fmt.Sprintf(`<a href="https://matrix.to/#/%s">%s</a>`, mxid, displayname))
		content.Body = strings.ReplaceAll(content.Body, number, displayname)
		if _, ok := alreadyMentioned[mxid]; !ok {
			alreadyMentioned[mxid] = struct{}{}
			content.Mentions.UserIDs = append(content.Mentions.UserIDs, mxid)
		}
	}
	if output != content.Body || forceHTML {
		output = strings.ReplaceAll(output, "\n", "<br/>")
		content.FormattedBody = output
		content.Format = event.FormatHTML
		for regex, replacer := range formatter.waReplFuncText {
			content.Body = regex.ReplaceAllStringFunc(content.Body, replacer)
		}
	}
}

func (formatter *Formatter) ParseMatrix(html string, mentions *event.Mentions) (string, []string) {
	ctx := format.NewContext()
	var mentionedJIDs []string
	if mentions != nil {
		var allowedMentions = make(map[types.JID]bool)
		mentionedJIDs = make([]string, 0, len(mentions.UserIDs))
		for _, userID := range mentions.UserIDs {
			var jid types.JID
			if puppet := formatter.bridge.GetPuppetByMXID(userID); puppet != nil {
				jid = puppet.JID
				mentionedJIDs = append(mentionedJIDs, puppet.JID.String())
			} else if user := formatter.bridge.GetUserByMXIDIfExists(userID); user != nil {
				jid = user.JID.ToNonAD()
			}
			if !jid.IsEmpty() && !allowedMentions[jid] {
				allowedMentions[jid] = true
				mentionedJIDs = append(mentionedJIDs, jid.String())
			}
		}
		ctx.ReturnData[allowedMentionsContextKey] = allowedMentions
	}
	result := formatter.matrixHTMLParser.Parse(html, ctx)
	if mentions == nil {
		mentionedJIDs, _ = ctx.ReturnData[mentionedJIDsContextKey].([]string)
		sort.Strings(mentionedJIDs)
		mentionedJIDs = slices.Compact(mentionedJIDs)
	}
	return result, mentionedJIDs
}

func (formatter *Formatter) ParseMatrixWithoutMentions(html string) string {
	ctx := format.NewContext()
	ctx.ReturnData[allowedMentionsContextKey] = map[types.JID]struct{}{}
	return formatter.matrixHTMLParser.Parse(html, ctx)
}
