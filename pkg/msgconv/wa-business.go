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
	tpl := tplMsg.GetHydratedTemplate()
	if tpl == nil {
		tpl = tplMsg.GetHydratedFourRowTemplate()
	}
	if tpl == nil {
		if interactiveMsg := tplMsg.GetInteractiveMessageTemplate(); interactiveMsg != nil {
			return mc.convertInteractiveMessage(ctx, info, interactiveMsg)
		}
		// TODO there are a few other types too
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				Body:    "Unsupported business message (template)",
				MsgType: event.MsgText,
			},
		}, tplMsg.GetContextInfo()
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
		convertedTitle, _ = mc.convertMediaMessage(ctx, title.DocumentMessage, "file attachment", info, false, nil)
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_ImageMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, title.ImageMessage, "photo", info, false, nil)
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_VideoMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, title.VideoMessage, "video attachment", info, false, nil)
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_LocationMessage:
		content = fmt.Sprintf("Unsupported location message\n\n%s", content)
	case *waE2E.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText:
		content = fmt.Sprintf("%s\n\n%s", title.HydratedTitleText, content)
	}

	converted := mc.postProcessBusinessMessage(content, convertedTitle)
	converted.Extra["fi.mau.whatsapp.hydrated_template_id"] = tpl.GetTemplateID()
	converted.Extra["fi.mau.whatsapp.business_message_type"] = "template"
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

func (mc *MessageConverter) convertInteractiveMessage(ctx context.Context, info *types.MessageInfo, msg *waE2E.InteractiveMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	content := msg.GetBody().GetText()
	switch interactive := msg.GetInteractiveMessage().(type) {
	case *waE2E.InteractiveMessage_ShopStorefrontMessage:
		content = fmt.Sprintf("%s\n\nUnsupported shop storefront message", content)
	case *waE2E.InteractiveMessage_CollectionMessage_:
		// There doesn't seem to be much content here, maybe it's just meant to show the body/header/footer?
	case *waE2E.InteractiveMessage_NativeFlowMessage_:
		if buttons := interactive.NativeFlowMessage.GetButtons(); len(buttons) > 0 {
			descriptions := make([]string, len(buttons))
			for i, button := range buttons {
				descriptions[i] = fmt.Sprintf("<%s>", button.GetName())
			}
			content = fmt.Sprintf("%s\n\n%s\nUse the WhatsApp app to click buttons", content, strings.Join(descriptions, " - "))
		}
	case *waE2E.InteractiveMessage_CarouselMessage_:
		content = fmt.Sprintf("%s\n\nUnsupported carousel message", content)
	}
	if footer := msg.GetFooter().GetText(); footer != "" {
		content = fmt.Sprintf("%s\n\n%s", content, footer)
	}
	if title := msg.GetHeader().GetTitle(); title != "" {
		if subtitle := msg.GetHeader().GetSubtitle(); subtitle != "" {
			title = fmt.Sprintf("%s\n%s", title, subtitle)
		}
		content = fmt.Sprintf("%s\n\n%s", title, content)
	}

	var convertedTitle *bridgev2.ConvertedMessagePart
	switch headerMedia := msg.GetHeader().GetMedia().(type) {
	case *waE2E.InteractiveMessage_Header_DocumentMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, headerMedia.DocumentMessage, "file attachment", info, false, nil)
	case *waE2E.InteractiveMessage_Header_ImageMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, headerMedia.ImageMessage, "photo", info, false, nil)
	case *waE2E.InteractiveMessage_Header_VideoMessage:
		convertedTitle, _ = mc.convertMediaMessage(ctx, headerMedia.VideoMessage, "video attachment", info, false, nil)
	case *waE2E.InteractiveMessage_Header_LocationMessage:
		content = fmt.Sprintf("Unsupported location message\n\n%s", content)
	case *waE2E.InteractiveMessage_Header_ProductMessage:
		content = fmt.Sprintf("Unsupported product message\n\n%s", content)
	case *waE2E.InteractiveMessage_Header_JPEGThumbnail:
		content = fmt.Sprintf("Unsupported thumbnail message\n\n%s", content)
	}

	converted := mc.postProcessBusinessMessage(content, convertedTitle)
	converted.Extra["fi.mau.whatsapp.business_message_type"] = "interactive"
	return converted, msg.GetContextInfo()
}

func (mc *MessageConverter) convertInteractiveResponseMessage(ctx context.Context, msg *waE2E.InteractiveResponseMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    msg.GetBody().GetText(),
			MsgType: event.MsgText,
		},
		Extra: map[string]any{
			"fi.mau.whatsapp.interactive_response_message": map[string]any{},
		},
	}, msg.GetContextInfo()
}

func (mc *MessageConverter) convertButtonsMessage(ctx context.Context, info *types.MessageInfo, msg *waE2E.ButtonsMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	content := msg.GetContentText()
	if buttons := msg.GetButtons(); len(buttons) > 0 {
		descriptions := make([]string, len(buttons))
		for i, button := range buttons {
			descriptions[i] = fmt.Sprintf("<%s>", button.GetButtonText().GetDisplayText())
		}
		content = fmt.Sprintf("%s\n\n%s\nUse the WhatsApp app to click buttons", content, strings.Join(descriptions, " - "))
	}
	if footer := msg.GetFooterText(); footer != "" {
		content = fmt.Sprintf("%s\n\n%s", content, footer)
	}
	var convertedHeader *bridgev2.ConvertedMessagePart
	switch header := msg.GetHeader().(type) {
	case *waE2E.ButtonsMessage_DocumentMessage:
		convertedHeader, _ = mc.convertMediaMessage(ctx, header.DocumentMessage, "file attachment", info, false, nil)
	case *waE2E.ButtonsMessage_ImageMessage:
		convertedHeader, _ = mc.convertMediaMessage(ctx, header.ImageMessage, "photo", info, false, nil)
	case *waE2E.ButtonsMessage_VideoMessage:
		convertedHeader, _ = mc.convertMediaMessage(ctx, header.VideoMessage, "video attachment", info, false, nil)
	case *waE2E.ButtonsMessage_LocationMessage:
		content = fmt.Sprintf("Unsupported location message\n\n%s", content)
	case *waE2E.ButtonsMessage_Text:
		content = fmt.Sprintf("%s\n\n%s", header.Text, content)
	}
	converted := mc.postProcessBusinessMessage(content, convertedHeader)
	converted.Extra["fi.mau.whatsapp.business_message_type"] = "buttons"
	return converted, msg.GetContextInfo()
}

func (mc *MessageConverter) convertButtonsResponseMessage(ctx context.Context, msg *waE2E.ButtonsResponseMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    msg.GetSelectedDisplayText(),
			MsgType: event.MsgText,
		},
		Extra: map[string]any{
			"fi.mau.whatsapp.buttons_response_message": map[string]any{
				"selected_button_id": msg.GetSelectedButtonID(),
			},
		},
	}, msg.GetContextInfo()
}

func (mc *MessageConverter) postProcessBusinessMessage(content string, headerMediaPart *bridgev2.ConvertedMessagePart) *bridgev2.ConvertedMessagePart {
	converted := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    content,
		},
	}
	mc.parseFormatting(converted.Content, true, false)
	if headerMediaPart != nil {
		converted.Content = headerMediaPart.Content
		converted.Extra = headerMediaPart.Extra
		converted.DBMetadata = headerMediaPart.DBMetadata
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
	return converted
}

func (mc *MessageConverter) convertListMessage(ctx context.Context, msg *waE2E.ListMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
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
	converted := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    body,
			MsgType: event.MsgText,
		},
		Extra: map[string]any{
			"fi.mau.whatsapp.business_message_type": "list",
		},
	}
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
