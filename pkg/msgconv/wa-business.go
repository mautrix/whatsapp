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
	"strings"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
)

func (mc *MessageConverter) convertTemplateMessage(ctx context.Context, info *types.MessageInfo, tplMsg *waE2E.TemplateMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	converted := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    "Unsupported business message",
			MsgType: event.MsgText,
		},
	}

	tpl := tplMsg.GetHydratedTemplate()
	if tpl == nil {
		return converted, tplMsg.GetContextInfo()
	}
	content := tpl.GetHydratedContentText()
	if buttons := tpl.GetHydratedButtons(); len(buttons) > 0 {
		addButtonText := false
		descriptions := make([]string, len(buttons))
		for i, rawButton := range buttons {
			switch button := rawButton.GetHydratedButton().(type) {
			case *waE2E.HydratedTemplateButton_QuickReplyButton:
				descriptions[i] = fmt.Sprintf("<%s>", button.QuickReplyButton.GetDisplayText())
				addButtonText = true
			case *waE2E.HydratedTemplateButton_UrlButton:
				descriptions[i] = fmt.Sprintf("[%s](%s)", button.UrlButton.GetDisplayText(), button.UrlButton.GetURL())
			case *waE2E.HydratedTemplateButton_CallButton:
				descriptions[i] = fmt.Sprintf("[%s](tel:%s)", button.CallButton.GetDisplayText(), button.CallButton.GetPhoneNumber())
			}
		}
		description := strings.Join(descriptions, " - ")
		if addButtonText {
			description += "\nUse the WhatsApp app to click buttons"
		}
		content = fmt.Sprintf("%s\n\n%s", content, description)
	}
	if footer := tpl.GetHydratedFooterText(); footer != "" {
		content = fmt.Sprintf("%s\n\n%s", content, footer)
	}

	var convertedTitle *bridgev2.ConvertedMessagePart
	switch title := tpl.GetTitle().(type) {
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_DocumentMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, title.DocumentMessage, "file attachment")
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_ImageMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, title.ImageMessage, "photo")
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_VideoMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, title.VideoMessage, "video attachment")
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_LocationMessage:
		content = fmt.Sprintf("Unsupported location message\n\n%s", content)
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText:
		content = fmt.Sprintf("%s\n\n%s", title.HydratedTitleText, content)
	}

	converted.Content.Body = content
	mc.parseFormatting(converted.Content, true, false)
	if convertedTitle != nil {
		converted.Content = convertedTitle.Content
		converted.Extra = convertedTitle.Extra
		converted.DBMetadata = convertedTitle.DBMetadata
		if content != "" {
			if converted.Content.FileName == "" || converted.Content.FileName == converted.Content.Body {
				converted.Content.FileName = converted.Content.Body
				converted.Content.Body = ""
			}
			if converted.Content.Body != "" {
				converted.Content.Body += "\n\n"
			}
			converted.Content.Body += content
			contentHTML := parseWAFormattingToHTML(content, true)
			if contentHTML != event.TextToHTML(content) || converted.Content.FormattedBody != "" {
				converted.Content.EnsureHasHTML()
				if converted.Content.FormattedBody != "" {
					converted.Content.FormattedBody += "<br><br>"
				}
				converted.Content.FormattedBody += contentHTML
			}
		}
	}
	if converted.Extra == nil {
		converted.Extra = make(map[string]any)
	}
	converted.Extra["fi.mau.whatsapp.hydrated_template_id"] = tpl.GetTemplateID()
	return converted, tplMsg.GetContextInfo()
}

func (mc *MessageConverter) convertTemplateButtonReplyMessage(ctx context.Context, msg *waE2E.TemplateButtonReplyMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    msg.GetSelectedDisplayText(),
			MsgType: event.MsgText,
		},
		Extra: map[string]any{
			"fi.mau.whatsapp.template_button_reply": map[string]any{
				"id":    msg.GetSelectedID(),
				"index": msg.GetSelectedIndex(),
			},
		},
	}, msg.GetContextInfo()
}

func (mc *MessageConverter) convertListMessage(ctx context.Context, msg *waE2E.ListMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	converted := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    "Unsupported business message",
			MsgType: event.MsgText,
		},
	}
	body := msg.GetDescription()
	if msg.GetTitle() != "" {
		if body == "" {
			body = msg.GetTitle()
		} else {
			body = fmt.Sprintf("%s\n\n%s", msg.GetTitle(), body)
		}
	}
	randomID := random.String(64)
	body = fmt.Sprintf("%s\n%s", body, randomID)
	if msg.GetFooterText() != "" {
		body = fmt.Sprintf("%s\n\n%s", body, msg.GetFooterText())
	}
	converted.Content.Body = body
	mc.parseFormatting(converted.Content, false, true)

	var optionsMarkdown strings.Builder
	_, _ = fmt.Fprintf(&optionsMarkdown, "#### %s\n", msg.GetButtonText())
	for _, section := range msg.GetSections() {
		nesting := ""
		if section.GetTitle() != "" {
			_, _ = fmt.Fprintf(&optionsMarkdown, "* %s\n", section.GetTitle())
			nesting = "  "
		}
		for _, row := range section.GetRows() {
			if row.GetDescription() != "" {
				_, _ = fmt.Fprintf(&optionsMarkdown, "%s* %s: %s\n", nesting, row.GetTitle(), row.GetDescription())
			} else {
				_, _ = fmt.Fprintf(&optionsMarkdown, "%s* %s\n", nesting, row.GetTitle())
			}
		}
	}
	optionsMarkdown.WriteString("\nUse the WhatsApp app to respond")
	rendered := format.RenderMarkdown(optionsMarkdown.String(), true, false)
	converted.Content.Body = strings.Replace(converted.Content.Body, randomID, rendered.Body, 1)
	converted.Content.FormattedBody = strings.Replace(converted.Content.FormattedBody, randomID, rendered.FormattedBody, 1)
	return converted, msg.GetContextInfo()
}

func (mc *MessageConverter) convertListResponseMessage(ctx context.Context, msg *waE2E.ListResponseMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	var body string
	if msg.GetTitle() != "" {
		if msg.GetDescription() != "" {
			body = fmt.Sprintf("%s\n\n%s", msg.GetTitle(), msg.GetDescription())
		} else {
			body = msg.GetTitle()
		}
	} else if msg.GetDescription() != "" {
		body = msg.GetDescription()
	} else {
		body = "Unsupported list reply message"
	}
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    body,
			MsgType: event.MsgText,
		},
		Extra: map[string]any{
			"fi.mau.whatsapp.list_reply": map[string]any{
				"row_id": msg.GetSingleSelectReply().GetSelectedRowID(),
			},
		},
	}, msg.GetContextInfo()
}
