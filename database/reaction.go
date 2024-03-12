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

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/dbutil"
)

type ReactionQuery struct {
	*dbutil.QueryHelper[*Reaction]
}

func newReaction(qh *dbutil.QueryHelper[*Reaction]) *Reaction {
	return &Reaction{qh: qh}
}

const (
	getReactionByTargetJIDQuery = `
		SELECT chat_jid, chat_receiver, target_jid, sender, mxid, jid FROM reaction
		WHERE chat_jid=$1 AND chat_receiver=$2 AND target_jid=$3 AND sender=$4
	`
	getReactionByMXIDQuery = `
		SELECT chat_jid, chat_receiver, target_jid, sender, mxid, jid FROM reaction
		WHERE mxid=$1
	`
	upsertReactionQuery = `
		INSERT INTO reaction (chat_jid, chat_receiver, target_jid, sender, mxid, jid)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (chat_jid, chat_receiver, target_jid, sender)
			DO UPDATE SET mxid=excluded.mxid, jid=excluded.jid
	`
	deleteReactionQuery = `
		DELETE FROM reaction WHERE chat_jid=$1 AND chat_receiver=$2 AND target_jid=$3 AND sender=$4
	`
)

func (rq *ReactionQuery) GetByTargetJID(ctx context.Context, chat PortalKey, jid types.MessageID, sender types.JID) (*Reaction, error) {
	return rq.QueryOne(ctx, getReactionByTargetJIDQuery, chat.JID, chat.Receiver, jid, sender.ToNonAD())
}

func (rq *ReactionQuery) GetByMXID(ctx context.Context, mxid id.EventID) (*Reaction, error) {
	return rq.QueryOne(ctx, getReactionByMXIDQuery, mxid)
}

type Reaction struct {
	qh *dbutil.QueryHelper[*Reaction]

	Chat      PortalKey
	TargetJID types.MessageID
	Sender    types.JID
	MXID      id.EventID
	JID       types.MessageID
}

func (reaction *Reaction) Scan(row dbutil.Scannable) (*Reaction, error) {
	return dbutil.ValueOrErr(reaction, row.Scan(&reaction.Chat.JID, &reaction.Chat.Receiver, &reaction.TargetJID, &reaction.Sender, &reaction.MXID, &reaction.JID))
}

func (reaction *Reaction) sqlVariables() []any {
	reaction.Sender = reaction.Sender.ToNonAD()
	return []any{reaction.Chat.JID, reaction.Chat.Receiver, reaction.TargetJID, reaction.Sender, reaction.MXID, reaction.JID}
}

func (reaction *Reaction) Upsert(ctx context.Context) error {
	return reaction.qh.Exec(ctx, upsertReactionQuery, reaction.sqlVariables()...)
}

func (reaction *Reaction) Delete(ctx context.Context) error {
	return reaction.qh.Exec(ctx, deleteReactionQuery, reaction.Chat.JID, reaction.Chat.Receiver, reaction.TargetJID, reaction.Sender)
}
