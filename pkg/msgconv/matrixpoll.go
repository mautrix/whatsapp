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
	"reflect"

	"maunium.net/go/mautrix/event"
)

var (
	TypeMSC3381PollStart    = event.Type{Class: event.MessageEventType, Type: "org.matrix.msc3381.poll.start"}
	TypeMSC3381PollResponse = event.Type{Class: event.MessageEventType, Type: "org.matrix.msc3381.poll.response"}
)

type PollResponseContent struct {
	RelatesTo  event.RelatesTo `json:"m.relates_to"`
	V1Response struct {
		Answers []string `json:"answers"`
	} `json:"org.matrix.msc3381.poll.response"`
	V2Selections []string `json:"org.matrix.msc3381.v2.selections"`
}

func (content *PollResponseContent) GetRelatesTo() *event.RelatesTo {
	return &content.RelatesTo
}

func (content *PollResponseContent) OptionalGetRelatesTo() *event.RelatesTo {
	if content.RelatesTo.Type == "" {
		return nil
	}
	return &content.RelatesTo
}

func (content *PollResponseContent) SetRelatesTo(rel *event.RelatesTo) {
	content.RelatesTo = *rel
}

type MSC1767Message struct {
	Text    string `json:"org.matrix.msc1767.text,omitempty"`
	HTML    string `json:"org.matrix.msc1767.html,omitempty"`
	Message []struct {
		MimeType string `json:"mimetype"`
		Body     string `json:"body"`
	} `json:"org.matrix.msc1767.message,omitempty"`
}

//lint:ignore U1000 Unused function
func msc1767ToWhatsApp(msg MSC1767Message) string {
	for _, part := range msg.Message {
		if part.MimeType == "text/html" && msg.HTML == "" {
			msg.HTML = part.Body
		} else if part.MimeType == "text/plain" && msg.Text == "" {
			msg.Text = part.Body
		}
	}
	if msg.HTML != "" {
		return parseWAFormattingToHTML(msg.HTML, false)
	}
	return msg.Text
}

type PollStartContent struct {
	RelatesTo *event.RelatesTo `json:"m.relates_to"`
	PollStart struct {
		Kind          string         `json:"kind"`
		MaxSelections int            `json:"max_selections"`
		Question      MSC1767Message `json:"question"`
		Answers       []struct {
			ID string `json:"id"`
			MSC1767Message
		} `json:"answers"`
	} `json:"org.matrix.msc3381.poll.start"`
}

func (content *PollStartContent) GetRelatesTo() *event.RelatesTo {
	if content.RelatesTo == nil {
		content.RelatesTo = &event.RelatesTo{}
	}
	return content.RelatesTo
}

func (content *PollStartContent) OptionalGetRelatesTo() *event.RelatesTo {
	return content.RelatesTo
}

func (content *PollStartContent) SetRelatesTo(rel *event.RelatesTo) {
	content.RelatesTo = rel
}

func init() {
	event.TypeMap[TypeMSC3381PollResponse] = reflect.TypeOf(PollResponseContent{})
	event.TypeMap[TypeMSC3381PollStart] = reflect.TypeOf(PollStartContent{})
}
