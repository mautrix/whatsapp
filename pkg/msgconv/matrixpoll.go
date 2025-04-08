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
	"errors"
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) msc1767ToWhatsApp(ctx context.Context, msg event.MSC1767Message, allowedMentions *event.Mentions) (string, []string) {
	for _, part := range msg.Message {
		if part.MimeType == "text/html" && msg.HTML == "" {
			msg.HTML = part.Body
		} else if part.MimeType == "text/plain" && msg.Text == "" {
			msg.Text = part.Body
		}
	}
	mentions := make([]string, 0)
	if msg.HTML != "" {
		parseCtx := format.NewContext(ctx)
		parseCtx.ReturnData["allowed_mentions"] = allowedMentions
		parseCtx.ReturnData["output_mentions"] = &mentions
		return mc.HTMLParser.Parse(msg.HTML, parseCtx), mentions
	}
	return msg.Text, mentions
}

var (
	errPollMissingQuestion = bridgev2.WrapErrorInStatus(errors.New("poll message is missing question")).WithIsCertain(true).WithErrorAsMessage().WithSendNotice(true).WithErrorReason(event.MessageStatusUnsupported)
	errPollDuplicateOption = bridgev2.WrapErrorInStatus(errors.New("poll options must be unique")).WithIsCertain(true).WithErrorAsMessage().WithSendNotice(true).WithErrorReason(event.MessageStatusUnsupported)
)

func (mc *MessageConverter) PollStartToWhatsApp(
	ctx context.Context,
	content *event.PollStartEventContent,
	replyTo *database.Message,
	portal *bridgev2.Portal,
) (*waE2E.Message, map[[32]byte]string, error) {
	maxAnswers := content.PollStart.MaxSelections
	if maxAnswers >= len(content.PollStart.Answers) || maxAnswers < 0 {
		maxAnswers = 0
	}
	contextInfo, err := mc.generateContextInfo(replyTo, portal)
	if err != nil {
		return nil, nil, err
	}
	var question string
	question, contextInfo.MentionedJID = mc.msc1767ToWhatsApp(ctx, content.PollStart.Question, content.Mentions)
	if len(question) == 0 {
		return nil, nil, errPollMissingQuestion
	}
	options := make([]*waE2E.PollCreationMessage_Option, len(content.PollStart.Answers))
	optionMap := make(map[[32]byte]string, len(options))
	for i, opt := range content.PollStart.Answers {
		body, _ := mc.msc1767ToWhatsApp(ctx, opt.MSC1767Message, &event.Mentions{})
		hash := sha256.Sum256([]byte(body))
		if _, alreadyExists := optionMap[hash]; alreadyExists {
			zerolog.Ctx(ctx).Warn().Str("option", body).Msg("Poll has duplicate options, rejecting")
			return nil, nil, errPollDuplicateOption
		}
		optionMap[hash] = opt.ID
		options[i] = &waE2E.PollCreationMessage_Option{
			OptionName: ptr.Ptr(body),
		}
	}
	return &waE2E.Message{
		PollCreationMessage: &waE2E.PollCreationMessage{
			Name:                   ptr.Ptr(question),
			Options:                options,
			SelectableOptionsCount: ptr.Ptr(uint32(maxAnswers)),
			ContextInfo:            contextInfo,
		},
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: random.Bytes(32),
		},
	}, optionMap, nil
}

func (mc *MessageConverter) PollVoteToWhatsApp(
	ctx context.Context,
	client *whatsmeow.Client,
	content *event.PollResponseEventContent,
	pollMsg *database.Message,
) (*waE2E.Message, error) {
	parsedMsgID, err := waid.ParseMessageID(pollMsg.ID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to parse message ID")
		return nil, fmt.Errorf("failed to parse message ID")
	}
	pollMsgInfo := MessageIDToInfo(client, parsedMsgID)
	pollMsgInfo.Type = "poll"
	optionHashes := make([][]byte, 0, len(content.Response.Answers))
	if pollMsg.Metadata.(*waid.MessageMetadata).IsMatrixPoll {
		mappedAnswers, err := mc.DB.PollOption.GetHashes(ctx, pollMsg.MXID, content.Response.Answers)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get poll option hashes from database")
			return nil, fmt.Errorf("failed to get poll option hashes")
		}
		for _, selection := range content.Response.Answers {
			hash, ok := mappedAnswers[selection]
			if ok {
				optionHashes = append(optionHashes, hash[:])
			} else {
				zerolog.Ctx(ctx).Warn().Str("option", selection).Msg("Didn't find hash for selected option")
			}
		}
	} else {
		for _, selection := range content.Response.Answers {
			hash, _ := hex.DecodeString(selection)
			if len(hash) == 32 {
				optionHashes = append(optionHashes, hash)
			}
		}
	}
	pollUpdate, err := client.EncryptPollVote(pollMsgInfo, &waE2E.PollVoteMessage{
		SelectedOptions: optionHashes,
	})
	return &waE2E.Message{PollUpdateMessage: pollUpdate}, err
}

func MessageIDToInfo(client *whatsmeow.Client, parsedMsgID *waid.ParsedMessageID) *types.MessageInfo {
	return &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     parsedMsgID.Chat,
			Sender:   parsedMsgID.Sender,
			IsFromMe: parsedMsgID.Sender.User == client.Store.GetLID().User || parsedMsgID.Sender.User == client.Store.GetJID().User,
			IsGroup:  parsedMsgID.Chat.Server == types.GroupServer,
		},
		ID: parsedMsgID.ID,
	}
}
