// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package msgconv

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func (mc *MessageConverter) convertTextMessage(ctx context.Context, msg *waE2E.Message) (part *bridgev2.ConvertedMessagePart, contextInfo *waE2E.ContextInfo) {
	part = &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    msg.GetConversation(),
		},
	}
	if len(msg.GetExtendedTextMessage().GetText()) > 0 {
		part.Content.Body = msg.GetExtendedTextMessage().GetText()
	}
	contextInfo = msg.GetExtendedTextMessage().GetContextInfo()
	mc.parseFormatting(part.Content, false, false)
	part.Content.BeeperLinkPreviews = mc.convertURLPreviewToBeeper(ctx, msg.GetExtendedTextMessage())
	return
}

func (mc *MessageConverter) parseFormatting(content *event.MessageEventContent, allowInlineURL, forceHTML bool) {
	parsedHTML := parseWAFormattingToHTML(content.Body, allowInlineURL)
	if forceHTML || parsedHTML != event.TextToHTML(content.Body) {
		content.FormattedBody = parsedHTML
		content.Format = event.FormatHTML
	}
}

var italicRegex = regexp.MustCompile(`([\s>~*]|^)_(.+?)_([^a-zA-Z\d]|$)`)
var boldRegex = regexp.MustCompile(`([\s>_~]|^)\*(.+?)\*([^a-zA-Z\d]|$)`)
var strikethroughRegex = regexp.MustCompile(`([\s>_*]|^)~(.+?)~([^a-zA-Z\d]|$)`)
var inlineCodeRegex = regexp.MustCompile("([\\s>_*~]|^)`(.+?)`([^a-zA-Z\\d]|$)")
var inlineURLRegex = regexp.MustCompile(`\[(.+?)]\((.+?)\)`)
var orderedListItemRegex = regexp.MustCompile(`^(\d{1,2})\. `)

var waReplString = map[*regexp.Regexp]string{
	italicRegex:        "$1<em>$2</em>$3",
	boldRegex:          "$1<strong>$2</strong>$3",
	strikethroughRegex: "$1<del>$2</del>$3",
	inlineCodeRegex:    "$1<code>$2</code>$3",
}

func parseWASubFormattingLineToHTML(text string, allowInlineURL bool) string {
	text = html.EscapeString(text)
	for regex, replacement := range waReplString {
		text = regex.ReplaceAllString(text, replacement)
	}
	if allowInlineURL {
		text = inlineURLRegex.ReplaceAllStringFunc(text, func(s string) string {
			groups := inlineURLRegex.FindStringSubmatch(s)
			return fmt.Sprintf(`<a href="%s">%s</a>`, groups[2], groups[1])
		})
	}
	return text
}

func parseWASubFormattingToHTML(text string, allowInlineURL bool, output *strings.Builder) {
	lines := strings.Split(text, "\n")
	orderedListIdx := -1
	inBulletedList := false
	wasBlockQuote := false
	for i, line := range lines {
		if i != 0 && orderedListIdx < 0 && !inBulletedList && !wasBlockQuote {
			output.WriteString("<br>")
		}
		wasBlockQuote = false
		if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") {
			if orderedListIdx >= 0 {
				orderedListIdx = -1
				output.WriteString("</ol>")
			}
			if !inBulletedList {
				output.WriteString("<ul>")
				inBulletedList = true
			}
			_, _ = fmt.Fprintf(output, "<li>%s</li>", parseWASubFormattingLineToHTML(line[2:], allowInlineURL))
		} else if match := orderedListItemRegex.FindStringSubmatch(line); match != nil {
			if inBulletedList {
				output.WriteString("</ul>")
				inBulletedList = false
			}
			newIndex, _ := strconv.Atoi(match[1])
			if orderedListIdx < 0 {
				if newIndex != 1 {
					_, _ = fmt.Fprintf(output, `<ol start="%s">`, match[1])
				} else {
					output.WriteString("<ol>")
				}
				orderedListIdx = newIndex - 1
			}
			parsedLine := parseWASubFormattingLineToHTML(strings.TrimPrefix(line, match[0]), allowInlineURL)
			if orderedListIdx+1 != newIndex {
				_, _ = fmt.Fprintf(output, `<li value="%s">%s</li>`, match[1], parsedLine)
			} else {
				_, _ = fmt.Fprintf(output, "<li>%s</li>", parsedLine)
			}
			orderedListIdx = newIndex
		} else if strings.HasPrefix(line, "> ") {
			if orderedListIdx >= 0 {
				orderedListIdx = -1
				output.WriteString("</ol>")
			} else if inBulletedList {
				output.WriteString("</ul>")
				inBulletedList = false
			}
			_, _ = fmt.Fprintf(output, "<blockquote>%s</blockquote>", parseWASubFormattingLineToHTML(line[2:], allowInlineURL))
			wasBlockQuote = true
		} else {
			if orderedListIdx >= 0 {
				orderedListIdx = -1
				output.WriteString("</ol>")
			} else if inBulletedList {
				output.WriteString("</ul>")
				inBulletedList = false
			}
			output.WriteString(parseWASubFormattingLineToHTML(line, allowInlineURL))
		}
	}
	if orderedListIdx >= 0 {
		output.WriteString("</ol>")
	} else if inBulletedList {
		output.WriteString("</ul>")
	}
}

func parseWAFormattingToHTML(text string, allowInlineURL bool) string {
	var output strings.Builder
	codeBlockPtr := 0
	for {
		relativeStartIdx := strings.Index(text[codeBlockPtr:], "```")
		if relativeStartIdx < 0 {
			break
		}
		absStartIdx := codeBlockPtr + relativeStartIdx
		relativeEndIdx := strings.Index(text[absStartIdx+3:], "```")
		if relativeEndIdx < 0 {
			break
		}
		absEndIdx := absStartIdx + 3 + relativeEndIdx + 3
		// Don't allow code blocks without content, but check for another ``` in case it's a code block containing ```.
		// No need to check more than once because there'll always be at least ``` in the code block after the second try.
		if strings.TrimSpace(text[absStartIdx+3:absEndIdx-3]) == "" {
			relativeEndIdx = strings.Index(text[absEndIdx:], "```")
			if relativeEndIdx < 0 {
				break
			}
			absEndIdx += relativeEndIdx + 3
		}
		prefix := text[codeBlockPtr:absStartIdx]
		content := text[absStartIdx+3 : absEndIdx-3]
		codeBlockPtr = absEndIdx
		if prefix != "" {
			parseWASubFormattingToHTML(prefix, allowInlineURL, &output)
		}
		if strings.ContainsRune(content, '\n') {
			_, _ = fmt.Fprintf(&output, "<pre><code>%s</code></pre>", html.EscapeString(content))
		} else {
			_, _ = fmt.Fprintf(&output, "<code>%s</code>", html.EscapeString(content))
		}
	}
	if codeBlockPtr < len(text) {
		parseWASubFormattingToHTML(text[codeBlockPtr:], allowInlineURL, &output)
	}
	return output.String()
}
