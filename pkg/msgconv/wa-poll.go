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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) convertPollCreationMessage(ctx context.Context, msg *waE2E.PollCreationMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	optionNames := make([]string, len(msg.GetOptions()))
	optionsListText := make([]string, len(optionNames))
	optionsListHTML := make([]string, len(optionNames))
	msc3381Answers := make([]map[string]any, len(optionNames))
	for i, opt := range msg.GetOptions() {
		optionNames[i] = opt.GetOptionName()
		optionsListText[i] = fmt.Sprintf("%d. %s\n", i+1, optionNames[i])
		optionsListHTML[i] = fmt.Sprintf("<li>%s</li>", event.TextToHTML(optionNames[i]))
		optionHash := sha256.Sum256([]byte(opt.GetOptionName()))
		optionHashStr := hex.EncodeToString(optionHash[:])
		msc3381Answers[i] = map[string]any{
			"id":                      optionHashStr,
			"org.matrix.msc1767.text": opt.GetOptionName(),
		}
	}
	body := fmt.Sprintf("%s\n\n%s\n\n(This message is a poll. Please open WhatsApp to vote.)", msg.GetName(), strings.Join(optionsListText, "\n"))
	formattedBody := fmt.Sprintf("<p>%s</p><ol>%s</ol><p>(This message is a poll. Please open WhatsApp to vote.)</p>", event.TextToHTML(msg.GetName()), strings.Join(optionsListHTML, ""))
	maxChoices := int(msg.GetSelectableOptionsCount())
	if maxChoices <= 0 {
		maxChoices = len(optionNames)
	}
	evtType := event.EventMessage
	if mc.ExtEvPolls {
		evtType = event.EventUnstablePollStart
	}

	return &bridgev2.ConvertedMessagePart{
		Type: evtType,
		Content: &event.MessageEventContent{
			Body:          body,
			MsgType:       event.MsgText,
			Format:        event.FormatHTML,
			FormattedBody: formattedBody,
		},
		Extra: map[string]any{
			// Custom metadata
			"fi.mau.whatsapp.poll": map[string]any{
				"option_names":             optionNames,
				"selectable_options_count": msg.GetSelectableOptionsCount(),
			},
			// Legacy extensible events
			"org.matrix.msc1767.message": []map[string]any{
				{"mimetype": "text/html", "body": formattedBody},
				{"mimetype": "text/plain", "body": body},
			},
			"org.matrix.msc3381.poll.start": map[string]any{
				"kind":           "org.matrix.msc3381.poll.disclosed",
				"max_selections": maxChoices,
				"question": map[string]any{
					"org.matrix.msc1767.text": msg.GetName(),
				},
				"answers": msc3381Answers,
			},
		},
	}, msg.GetContextInfo()
}

func KeyToMessageID(client *whatsmeow.Client, chat, sender types.JID, key *waCommon.MessageKey) networkid.MessageID {
	sender = sender.ToNonAD()
	var err error
	if !key.GetFromMe() {
		if key.GetParticipant() != "" {
			sender, err = types.ParseJID(key.GetParticipant())
			if err != nil {
				// TODO log somehow?
				return ""
			}
			if sender.Server == types.LegacyUserServer {
				sender.Server = types.DefaultUserServer
			}
		} else if chat.Server == types.DefaultUserServer || chat.Server == types.BotServer {
			ownID := ptr.Val(client.Store.ID).ToNonAD()
			if sender.User == ownID.User {
				sender = chat
			} else {
				sender = ownID
			}
		} else {
			// TODO log somehow?
			return ""
		}
	}
	remoteJID, err := types.ParseJID(key.GetRemoteJID())
	if err == nil && !remoteJID.IsEmpty() {
		// TODO use remote jid in other cases?
		if remoteJID.Server == types.GroupServer {
			chat = remoteJID
		}
	}
	return waid.MakeMessageID(chat, sender, key.GetID())
}

var failedPollUpdatePart = &bridgev2.ConvertedMessagePart{
	Type:       event.EventUnstablePollResponse,
	Content:    &event.MessageEventContent{},
	DontBridge: true,
}

func (mc *MessageConverter) convertPollUpdateMessage(ctx context.Context, info *types.MessageInfo, msg *waE2E.PollUpdateMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	log := zerolog.Ctx(ctx)
	pollMessageID := KeyToMessageID(getClient(ctx), info.Chat, info.Sender, msg.PollCreationMessageKey)
	pollMessage, err := mc.Bridge.DB.Message.GetPartByID(ctx, getPortal(ctx).Receiver, pollMessageID, "")
	if err != nil {
		log.Err(err).Msg("Failed to get poll update target message")
		return failedPollUpdatePart, nil
	}
	vote, err := getClient(ctx).DecryptPollVote(ctx, &events.Message{
		Info:    *info,
		Message: &waE2E.Message{PollUpdateMessage: msg},
	})
	if err != nil {
		log.Err(err).Msg("Failed to decrypt vote message")
		return failedPollUpdatePart, nil
	}

	selectedHashes := make([]string, len(vote.GetSelectedOptions()))
	if pollMessage.Metadata.(*waid.MessageMetadata).IsMatrixPoll {
		mappedAnswers, err := mc.DB.PollOption.GetIDs(ctx, pollMessage.MXID, vote.GetSelectedOptions())
		if err != nil {
			log.Err(err).Msg("Failed to get poll option IDs")
			return failedPollUpdatePart, nil
		}
		for i, opt := range vote.GetSelectedOptions() {
			if len(opt) != 32 {
				log.Warn().Int("hash_len", len(opt)).Msg("Unexpected option hash length in vote")
				continue
			}
			var ok bool
			selectedHashes[i], ok = mappedAnswers[[32]byte(opt)]
			if !ok {
				log.Warn().Hex("option_hash", opt).Msg("Didn't find ID for option in vote")
			}
		}
	} else {
		for i, opt := range vote.GetSelectedOptions() {
			selectedHashes[i] = hex.EncodeToString(opt)
		}
	}
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventUnstablePollResponse,
		Content: &event.MessageEventContent{
			RelatesTo: &event.RelatesTo{
				Type:    event.RelReference,
				EventID: pollMessage.MXID,
			},
		},
		Extra: map[string]any{
			"org.matrix.msc3381.poll.response": map[string]any{
				"answers": selectedHashes,
			},
		},
	}, nil
}
