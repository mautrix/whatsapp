// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/rs/zerolog"
	"github.com/yuin/goldmark"
	"go.mau.fi/whatsmeow/types/richresponse"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
)

func (mc *MessageConverter) convertUnifiedRichResponseMessage(ctx context.Context, data []byte) *bridgev2.ConvertedMessagePart {
	extra := map[string]any{}
	if json.Valid(data) && len(data) < 24*1024 {
		extra["fi.mau.whatsapp.rich_message"] = json.RawMessage(data)
	} else if len(data) < 16*1024 {
		extra["fi.mau.whatsapp.non_json_rich_message"] = base64.StdEncoding.EncodeToString(data)
	}
	var rrMsg richresponse.RichResponse
	err := json.Unmarshal(data, &rrMsg)
	if err != nil || len(rrMsg.Sections) == 0 {
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to unmarshal unified rich response message")
		}
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Unknown message type, please view it on the WhatsApp app",
			},
			Extra: extra,
		}
	}
	extra["fi.mau.whatsapp.rich_response_id"] = rrMsg.ResponseID
	var htmlBuf strings.Builder
	for _, mv := range rrMsg.Sections {
		for _, p := range mv.Model.GetPrimitives() {
			mc.convertRichResponsePrimitive(ctx, p, &htmlBuf)
		}
	}
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: new(format.HTMLToContent(htmlBuf.String())),
		Extra:   extra,
	}
}

var mdRender = goldmark.New(format.Extensions, format.HTMLOptions, goldmark.WithExtensions(mdext.EscapeHTML))

func (mc *MessageConverter) convertRichMarkdownText(ctx context.Context, text *richresponse.GenAIMarkdownTextUXPrimitive, buf *strings.Builder) error {
	for _, ent := range text.InlineEntities {
		switch meta := ent.Metadata.(type) {
		//case *richresponse.GenAISearchCitationItem:
		case *richresponse.GenAIInlineLinkItem:
			text.Text = strings.ReplaceAll(
				text.Text,
				fmt.Sprintf("{{%s}}", ent.Key),
				fmt.Sprintf("[%s](%s)", meta.DisplayName, meta.URL),
			)
		case *richresponse.GenAIDeepLinkItem:
			text.Text = strings.ReplaceAll(
				text.Text,
				fmt.Sprintf("{{%s}}", ent.Key),
				meta.Text,
			)
		case *richresponse.GenAILatexItem:
			text.Text = strings.ReplaceAll(
				text.Text,
				fmt.Sprintf("{{%s}}", ent.Key),
				fmt.Sprintf(`<span data-mx-maths="%[1]s"><code>%[1]s</code></span>`, html.EscapeString(meta.LatexExpression)),
			)
		default:
			text.Text = strings.ReplaceAll(
				text.Text,
				fmt.Sprintf("{{%s}}", ent.Key),
				ent.Key,
			)
		}
	}
	return mdRender.Convert([]byte(text.Text), buf)
}

func (mc *MessageConverter) convertRichResponsePrimitive(ctx context.Context, primitive richresponse.Primitive, buf *strings.Builder) error {
	switch p := primitive.(type) {
	case *richresponse.GenAICodeUXPrimitive:
		if p.Language != "" {
			_, _ = fmt.Fprintf(buf, `<pre><code class="language-%s">`, html.EscapeString(p.Language))
		} else {
			buf.WriteString("<pre><code>")
		}
		for _, part := range p.CodeBlocks {
			buf.WriteString(part.Content)
		}
		buf.WriteString("</code></pre>")
	case *richresponse.GenAIMarkdownTextUXPrimitive:
		return mc.convertRichMarkdownText(ctx, p, buf)
	case *richresponse.GenATableUXPrimitive:
		buf.WriteString("<table>")
		for _, row := range p.Rows {
			buf.WriteString("<tr>")
			cellType := "tr"
			if row.IsHeader {
				cellType = "th"
			}
			if len(row.MarkdownCells) > 0 {
				for _, cell := range row.MarkdownCells {
					_, _ = fmt.Fprintf(buf, "<%s>", cellType)
					err := mc.convertRichMarkdownText(ctx, &cell, buf)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintf(buf, "</%s>", cellType)
				}
			} else {
				for _, cell := range row.Cells {
					_, _ = fmt.Fprintf(buf, "<%s>%s</%s>", cellType, html.EscapeString(cell), cellType)
				}
			}
			buf.WriteString("</tr>")
		}
		buf.WriteString("</table>")
	case *richresponse.GenAILatexUXPrimitive:
		_, _ = fmt.Fprintf(buf, `<span data-mx-maths="%[1]s"><code>%[1]s</code></span>`, html.EscapeString(p.GetLatexExpression()))
	case *richresponse.FOATextPrimitive:
		// TODO remove placeholders?
		return mdRender.Convert([]byte(p.Text), buf)
	case *richresponse.GenAIMetadataTextPrimitive:
		return mdRender.Convert([]byte(p.Text), buf)
	//case *richresponse.GenAIBotThinkingStatusPrimitive:
	//case *richresponse.GenAIProductItemCardPrimitive:
	//case *richresponse.GenAIImagePrimitive:
	//case *richresponse.GenAITaskPrimitive:
	//case *richresponse.GenAIReelPrimitive:
	//case *richresponse.GenAIPostPrimitive:
	//case *richresponse.GenAIImaginePrimitive:
	//case *richresponse.GenAISearchResultPrimitive:
	//case *richresponse.FOABloksPrimitive:
	case *richresponse.GenAIDividerPrimitive:
		// TODO dots?
		buf.WriteString("<hr>")
	case *richresponse.GenAISpacerPrimitive:
		if p.Spacing <= 1 {
			buf.WriteString("<hr>")
		} else {
			for range p.Spacing {
				buf.WriteString("<br>")
			}
		}
	default:
		buf.WriteString("<p>Unknown primitive type</p>")
	}
	return nil
}
