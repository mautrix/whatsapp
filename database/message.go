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

package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/id"
)

type MessageQuery struct {
	*dbutil.QueryHelper[*Message]
}

func newMessage(qh *dbutil.QueryHelper[*Message]) *Message {
	return &Message{qh: qh}
}

const (
	getAllMessagesQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2
	`
	getMessageByJIDQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND jid=$3
	`
	getMessageByMXIDQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE mxid=$1
	`
	getLastMessageInChatQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND timestamp<=$3 AND sent=true ORDER BY timestamp DESC LIMIT 1
	`
	getFirstMessageInChatQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND sent=true ORDER BY timestamp ASC LIMIT 1
	`
	getMessagesBetweenQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND timestamp>$3 AND timestamp<=$4 AND sent=true AND error='' ORDER BY timestamp ASC
	`
	insertMessageQuery = `
		INSERT INTO message
			(chat_jid, chat_receiver, jid, mxid, sender, sender_mxid, timestamp, sent, type, error, broadcast_list_jid)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	markMessageSentQuery   = "UPDATE message SET sent=true, timestamp=$1 WHERE chat_jid=$2 AND chat_receiver=$3 AND jid=$4"
	updateMessageMXIDQuery = "UPDATE message SET mxid=$1, type=$2, error=$3 WHERE chat_jid=$4 AND chat_receiver=$5 AND jid=$6"
	deleteMessageQuery     = "DELETE FROM message WHERE chat_jid=$1 AND chat_receiver=$2 AND jid=$3"
)

func (mq *MessageQuery) GetAll(ctx context.Context, chat PortalKey) ([]*Message, error) {
	return mq.QueryMany(ctx, getAllMessagesQuery, chat.JID, chat.Receiver)
}

func (mq *MessageQuery) GetByJID(ctx context.Context, chat PortalKey, jid types.MessageID) (*Message, error) {
	return mq.QueryOne(ctx, getMessageByJIDQuery, chat.JID, chat.Receiver, jid)
}

func (mq *MessageQuery) GetByMXID(ctx context.Context, mxid id.EventID) (*Message, error) {
	return mq.QueryOne(ctx, getMessageByMXIDQuery, mxid)
}

func (mq *MessageQuery) GetLastInChat(ctx context.Context, chat PortalKey) (*Message, error) {
	return mq.GetLastInChatBefore(ctx, chat, time.Now().Add(60*time.Second))
}

func (mq *MessageQuery) GetLastInChatBefore(ctx context.Context, chat PortalKey, maxTimestamp time.Time) (*Message, error) {
	msg, err := mq.QueryOne(ctx, getLastMessageInChatQuery, chat.JID, chat.Receiver, maxTimestamp.Unix())
	if msg != nil && msg.Timestamp.IsZero() {
		// Old db, we don't know what the last message is.
		msg = nil
	}
	return msg, err
}

func (mq *MessageQuery) GetFirstInChat(ctx context.Context, chat PortalKey) (*Message, error) {
	return mq.QueryOne(ctx, getFirstMessageInChatQuery, chat.JID, chat.Receiver)
}

func (mq *MessageQuery) GetMessagesBetween(ctx context.Context, chat PortalKey, minTimestamp, maxTimestamp time.Time) ([]*Message, error) {
	return mq.QueryMany(ctx, getMessagesBetweenQuery, chat.JID, chat.Receiver, minTimestamp.Unix(), maxTimestamp.Unix())
}

type MessageErrorType string

const (
	MsgNoError             MessageErrorType = ""
	MsgErrDecryptionFailed MessageErrorType = "decryption_failed"
	MsgErrMediaNotFound    MessageErrorType = "media_not_found"
)

type MessageType string

const (
	MsgUnknown       MessageType = ""
	MsgFake          MessageType = "fake"
	MsgNormal        MessageType = "message"
	MsgReaction      MessageType = "reaction"
	MsgEdit          MessageType = "edit"
	MsgMatrixPoll    MessageType = "matrix-poll"
	MsgBeeperGallery MessageType = "beeper-gallery"
)

type Message struct {
	qh *dbutil.QueryHelper[*Message]

	Chat       PortalKey
	JID        types.MessageID
	MXID       id.EventID
	Sender     types.JID
	SenderMXID id.UserID
	Timestamp  time.Time
	Sent       bool
	Type       MessageType
	Error      MessageErrorType

	GalleryPart int

	BroadcastListJID types.JID
}

func (msg *Message) IsFakeMXID() bool {
	return strings.HasPrefix(msg.MXID.String(), "net.maunium.whatsapp.fake::")
}

func (msg *Message) IsFakeJID() bool {
	return strings.HasPrefix(msg.JID, "FAKE::") || msg.JID == string(msg.MXID)
}

const fakeGalleryMXIDFormat = "com.beeper.gallery::%d:%s"

func (msg *Message) Scan(row dbutil.Scannable) (*Message, error) {
	var ts int64
	err := row.Scan(&msg.Chat.JID, &msg.Chat.Receiver, &msg.JID, &msg.MXID, &msg.Sender, &msg.SenderMXID, &ts, &msg.Sent, &msg.Type, &msg.Error, &msg.BroadcastListJID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(msg.MXID.String(), "com.beeper.gallery::") {
		_, err = fmt.Sscanf(msg.MXID.String(), fakeGalleryMXIDFormat, &msg.GalleryPart, &msg.MXID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse gallery MXID: %w", err)
		}
	}
	if ts != 0 {
		msg.Timestamp = time.Unix(ts, 0)
	}
	return msg, nil
}

func (msg *Message) sqlVariables() []any {
	mxid := msg.MXID.String()
	if msg.GalleryPart != 0 {
		mxid = fmt.Sprintf(fakeGalleryMXIDFormat, msg.GalleryPart, mxid)
	}
	return []any{msg.Chat.JID, msg.Chat.Receiver, msg.JID, mxid, msg.Sender, msg.SenderMXID, msg.Timestamp.Unix(), msg.Sent, msg.Type, msg.Error, msg.BroadcastListJID}
}

func (msg *Message) Insert(ctx context.Context) error {
	return msg.qh.Exec(ctx, insertMessageQuery, msg.sqlVariables()...)
}

func (msg *Message) MarkSent(ctx context.Context, ts time.Time) error {
	msg.Sent = true
	msg.Timestamp = ts
	return msg.qh.Exec(ctx, markMessageSentQuery, ts.Unix(), msg.Chat.JID, msg.Chat.Receiver, msg.JID)
}

func (msg *Message) UpdateMXID(ctx context.Context, mxid id.EventID, newType MessageType, newError MessageErrorType) error {
	msg.MXID = mxid
	msg.Type = newType
	msg.Error = newError
	return msg.qh.Exec(ctx, updateMessageMXIDQuery, mxid, newType, newError, msg.Chat.JID, msg.Chat.Receiver, msg.JID)
}

func (msg *Message) Delete(ctx context.Context) error {
	return msg.qh.Exec(ctx, deleteMessageQuery, msg.Chat.JID, msg.Chat.Receiver, msg.JID)
}
