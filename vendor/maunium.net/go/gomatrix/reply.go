package gomatrix

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var HTMLReplyFallbackRegex = regexp.MustCompile(`^<mx-reply>[\s\S]+?</mx-reply>`)

func TrimReplyFallbackHTML(html string) string {
	return HTMLReplyFallbackRegex.ReplaceAllString(html, "")
}

func TrimReplyFallbackText(text string) string {
	if !strings.HasPrefix(text, "> ") || !strings.Contains(text, "\n") {
		return text
	}

	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[0], "> ") {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (content *Content) RemoveReplyFallback() {
	if len(content.GetReplyTo()) > 0 {
		if content.Format == FormatHTML {
			content.FormattedBody = TrimReplyFallbackHTML(content.FormattedBody)
		}
		content.Body = TrimReplyFallbackText(content.Body)
	}
}

func (content *Content) GetReplyTo() string {
	if content.RelatesTo != nil {
		return content.RelatesTo.InReplyTo.EventID
	}
	return ""
}

const ReplyFormat = `<mx-reply><blockquote>
<a href="https://matrix.to/#/%s/%s">In reply to</a>
<a href="https://matrix.to/#/%s">%s</a>
%s
</blockquote></mx-reply>
`

func (evt *Event) GenerateReplyFallbackHTML() string {
	body := evt.Content.FormattedBody
	if len(body) == 0 {
		body = html.EscapeString(evt.Content.Body)
	}

	senderDisplayName := evt.Sender

	return fmt.Sprintf(ReplyFormat, evt.RoomID, evt.ID, evt.Sender, senderDisplayName, body)
}

func (evt *Event) GenerateReplyFallbackText() string {
	body := evt.Content.Body
	lines := strings.Split(strings.TrimSpace(body), "\n")
	firstLine, lines := lines[0], lines[1:]

	senderDisplayName := evt.Sender

	var fallbackText strings.Builder
	fmt.Fprintf(&fallbackText, "> <%s> %s", senderDisplayName, firstLine)
	for _, line := range lines {
		fmt.Fprintf(&fallbackText, "\n> %s", line)
	}
	fallbackText.WriteString("\n\n")
	return fallbackText.String()
}

func (content *Content) SetReply(inReplyTo *Event) {
	if content.RelatesTo == nil {
		content.RelatesTo = &RelatesTo{}
	}
	content.RelatesTo.InReplyTo = InReplyTo{
		EventID: inReplyTo.ID,
		RoomID:  inReplyTo.RoomID,
	}

	if content.MsgType == MsgText || content.MsgType == MsgNotice {
		if len(content.FormattedBody) == 0 || content.Format != FormatHTML {
			content.FormattedBody = html.EscapeString(content.Body)
			content.Format = FormatHTML
		}
		content.FormattedBody = inReplyTo.GenerateReplyFallbackHTML() + content.FormattedBody
		content.Body = inReplyTo.GenerateReplyFallbackText() + content.Body
	}
}
