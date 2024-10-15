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
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exerrors"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) convertUnknownMessage(ctx context.Context, msg *waE2E.Message) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	data, _ := proto.Marshal(msg)
	encodedMsg := base64.StdEncoding.EncodeToString(data)
	extra := make(map[string]any)
	if len(encodedMsg) < 16*1024 {
		extra["fi.mau.whatsapp.unsupported_message_data"] = encodedMsg
	}
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    "Unknown message type, please view it on the WhatsApp app",
		},
		Extra: extra,
	}, nil
}

var otpContent = format.RenderMarkdown("You received a one-time passcode. For added security, you can only see it on your primary device for WhatsApp. [Learn more](https://faq.whatsapp.com/372839278914311)", true, false)

func init() {
	otpContent.MsgType = event.MsgNotice
}

func (mc *MessageConverter) convertPlaceholderMessage(ctx context.Context, rawMsg *waE2E.Message) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	if rawMsg.GetPlaceholderMessage().GetType() == waE2E.PlaceholderMessage_MASK_LINKED_DEVICES {
		return &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: ptr.Clone(&otpContent),
		}, nil
	} else {
		return mc.convertUnknownMessage(ctx, rawMsg)
	}
}

const inviteMsg = `%s<hr/>This invitation to join "%s" expires at %s. Reply to this message with <code>%s accept</code> to accept the invite.`
const inviteMsgBroken = `%s<hr/>This invitation to join "%s" expires at %s. However, the invite message is broken or unsupported and cannot be accepted.`
const GroupInviteMetaField = "fi.mau.whatsapp.invite"

func (mc *MessageConverter) convertGroupInviteMessage(ctx context.Context, info *types.MessageInfo, msg *waE2E.GroupInviteMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	expiry := time.Unix(msg.GetInviteExpiration(), 0)
	template := inviteMsg
	var extraAttrs map[string]any
	var inviteMeta *waid.GroupInviteMeta
	groupJID, err := types.ParseJID(msg.GetGroupJID())
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("invite_group_jid", msg.GetGroupJID()).Msg("Failed to parse invite group JID")
		template = inviteMsgBroken
	} else {
		inviteMeta = &waid.GroupInviteMeta{
			JID:        groupJID,
			Code:       msg.GetInviteCode(),
			Expiration: msg.GetInviteExpiration(),
			Inviter:    info.Sender.ToNonAD(),
		}
		extraAttrs = map[string]any{
			GroupInviteMetaField: inviteMeta,
		}
	}

	htmlMessage := fmt.Sprintf(template, event.TextToHTML(msg.GetCaption()), msg.GetGroupName(), expiry, mc.Bridge.Config.CommandPrefix)
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          format.HTMLToText(htmlMessage),
		Format:        event.FormatHTML,
		FormattedBody: htmlMessage,
	}
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
		Extra:   extraAttrs,
		DBMetadata: &waid.MessageMetadata{
			GroupInvite: inviteMeta,
		},
	}, msg.GetContextInfo()
}

func (mc *MessageConverter) convertEphemeralSettingMessage(ctx context.Context, msg *waE2E.ProtocolMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	portal := getPortal(ctx)
	portalMeta := portal.Metadata.(*waid.PortalMetadata)
	disappear := database.DisappearingSetting{
		Type:  database.DisappearingTypeAfterRead,
		Timer: time.Duration(msg.GetEphemeralExpiration()) * time.Second,
	}
	if disappear.Timer == 0 {
		disappear.Type = ""
	}
	dontBridge := portal.Disappear == disappear
	content := bridgev2.DisappearingMessageNotice(disappear.Timer, false)
	if msg.EphemeralSettingTimestamp == nil || portalMeta.DisappearingTimerSetAt < msg.GetEphemeralSettingTimestamp() {
		portal.Disappear = disappear
		portalMeta.DisappearingTimerSetAt = msg.GetEphemeralSettingTimestamp()
		err := portal.Save(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to save portal after updating expiration timer")
		}
	} else {
		content.Body += ", but the change was ignored."
	}
	return &bridgev2.ConvertedMessagePart{
		Type:       event.EventMessage,
		Content:    content,
		DontBridge: dontBridge,
	}, nil
}

const eventMessageTemplate = `
{{- if .Name -}}
	<h4>{{ .Name }}</h4>
{{- end -}}
{{- if .StartTime -}}
	<p>
		Start time: <time datetime="{{ .StartTimeISO }}">{{ .StartTime }}</time>
		{{- if .EndTime -}}
			<br>
			End time: <time datetime="{{ .EndTimeISO }}">{{ .EndTime }}</time>
		{{- end -}}
	</p>
{{- end -}}
{{- if .Location -}}
	<p>Location: {{ .Location }}</p>
{{- end -}}
{{- if .DescriptionHTML -}}
	<p>{{ .DescriptionHTML }}</p>
{{- end -}}
{{- if .JoinLink -}}
	<p>Join link: <a href="{{ .JoinLink }}">{{ .JoinLink }}</a></p>
{{- end -}}
`

var eventMessageTplParsed = exerrors.Must(template.New("eventmessage").Parse(strings.TrimSpace(eventMessageTemplate)))

type eventMessageParams struct {
	Name            string
	JoinLink        string
	StartTimeISO    string
	StartTime       string
	EndTimeISO      string
	EndTime         string
	Location        string
	DescriptionHTML template.HTML
}

func (mc *MessageConverter) convertEventMessage(ctx context.Context, msg *waE2E.EventMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	params := &eventMessageParams{
		Name:            msg.GetName(),
		JoinLink:        msg.GetJoinLink(),
		Location:        msg.GetLocation().GetName(),
		DescriptionHTML: template.HTML(parseWAFormattingToHTML(msg.GetDescription(), false)),
	}
	if msg.StartTime != nil {
		startTS := time.Unix(msg.GetStartTime(), 0)
		params.StartTime = startTS.Format(time.RFC1123)
		params.StartTimeISO = startTS.Format(time.RFC3339)
	}
	if msg.EndTime != nil {
		endTS := time.Unix(msg.GetEndTime(), 0)
		params.EndTime = endTS.Format(time.RFC1123)
		params.EndTimeISO = endTS.Format(time.RFC3339)
	}
	var buf strings.Builder
	err := eventMessageTplParsed.Execute(&buf, params)
	var content event.MessageEventContent
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to execute event message template")
		content = event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    "Failed to parse event message",
		}
	} else {
		content = format.HTMLToContent(buf.String())
	}
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}, msg.GetContextInfo()
}
