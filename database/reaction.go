// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/whatsmeow/types"
)

type ReactionQuery struct {
	db  *Database
	log log.Logger
}

func (rq *ReactionQuery) New() *Reaction {
	return &Reaction{
		db:  rq.db,
		log: rq.log,
	}
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
		DELETE FROM reaction WHERE chat_jid=$1 AND chat_receiver=$2 AND target_jid=$3 AND sender=$4 AND mxid=$5
	`
)

func (rq *ReactionQuery) GetByTargetJID(chat PortalKey, jid types.MessageID, sender types.JID) *Reaction {
	return rq.maybeScan(rq.db.QueryRow(getReactionByTargetJIDQuery, chat.JID, chat.Receiver, jid, sender.ToNonAD()))
}

func (rq *ReactionQuery) GetByMXID(mxid id.EventID) *Reaction {
	return rq.maybeScan(rq.db.QueryRow(getReactionByMXIDQuery, mxid))
}

func (rq *ReactionQuery) maybeScan(row *sql.Row) *Reaction {
	if row == nil {
		return nil
	}
	return rq.New().Scan(row)
}

type Reaction struct {
	db  *Database
	log log.Logger

	Chat      PortalKey
	TargetJID types.MessageID
	Sender    types.JID
	MXID      id.EventID
	JID       types.MessageID
}

func (reaction *Reaction) Scan(row dbutil.Scannable) *Reaction {
	err := row.Scan(&reaction.Chat.JID, &reaction.Chat.Receiver, &reaction.TargetJID, &reaction.Sender, &reaction.MXID, &reaction.JID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			reaction.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return reaction
}

func (reaction *Reaction) Upsert() {
	reaction.Sender = reaction.Sender.ToNonAD()
	_, err := reaction.db.Exec(upsertReactionQuery, reaction.Chat.JID, reaction.Chat.Receiver, reaction.TargetJID, reaction.Sender, reaction.MXID, reaction.JID)
	if err != nil {
		reaction.log.Warnfln("Failed to upsert reaction to %s@%s by %s: %v", reaction.Chat, reaction.TargetJID, reaction.Sender, err)
	}
}

func (reaction *Reaction) GetTarget() *Message {
	return reaction.db.Message.GetByJID(reaction.Chat, reaction.TargetJID)
}

func (reaction *Reaction) Delete() {
	_, err := reaction.db.Exec(deleteReactionQuery, reaction.Chat.JID, reaction.Chat.Receiver, reaction.TargetJID, reaction.Sender, reaction.MXID)
	if err != nil {
		reaction.log.Warnfln("Failed to delete reaction %s: %v", reaction.MXID, err)
	}
}
