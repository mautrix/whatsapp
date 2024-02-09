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
	"fmt"
	"strings"

	"github.com/lib/pq"
	"go.mau.fi/util/dbutil"
)

func scanPollOptionMapping(rows dbutil.Rows) (id string, hashArr [32]byte, err error) {
	var hash []byte
	err = rows.Scan(&id, &hash)
	if err != nil {
		// return below
	} else if len(hash) != 32 {
		err = fmt.Errorf("unexpected hash length %d", len(hash))
	} else {
		hashArr = *(*[32]byte)(hash)
	}
	return
}

func (msg *Message) PutPollOptions(opts map[[32]byte]string) {
	query := "INSERT INTO poll_option_id (msg_mxid, opt_id, opt_hash) VALUES ($1, $2, $3)"
	args := make([]any, len(opts)*2+1)
	placeholders := make([]string, len(opts))
	args[0] = msg.MXID
	i := 0
	for hash, id := range opts {
		args[i*2+1] = id
		hashCopy := hash
		args[i*2+2] = hashCopy[:]
		placeholders[i] = fmt.Sprintf("($1, $%d, $%d)", i*2+2, i*2+3)
		i++
	}
	query = strings.ReplaceAll(query, "($1, $2, $3)", strings.Join(placeholders, ","))
	_, err := msg.db.Exec(query, args...)
	if err != nil {
		msg.log.Errorfln("Failed to save poll options for %s: %v", msg.MXID, err)
	}
}

func (msg *Message) GetPollOptionIDs(hashes [][]byte) map[[32]byte]string {
	query := "SELECT opt_id, opt_hash FROM poll_option_id WHERE msg_mxid=$1 AND opt_hash = ANY($2)"
	var args []any
	if msg.db.Dialect == dbutil.Postgres {
		args = []any{msg.MXID, pq.Array(hashes)}
	} else {
		query = strings.ReplaceAll(query, " = ANY($2)", fmt.Sprintf(" IN (%s)", strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")))
		args = make([]any, len(hashes)+1)
		args[0] = msg.MXID
		for i, hash := range hashes {
			args[i+1] = hash
		}
	}
	ids := make(map[[32]byte]string, len(hashes))
	rows, err := msg.db.Query(query, args...)
	if err != nil {
		msg.log.Errorfln("Failed to query poll option IDs for %s: %v", msg.MXID, err)
	} else {
		for rows.Next() {
			id, hash, err := scanPollOptionMapping(rows)
			if err != nil {
				msg.log.Errorfln("Failed to scan poll option ID for %s: %v", msg.MXID, err)
				break
			}
			ids[hash] = id
		}
	}
	return ids
}

func (msg *Message) GetPollOptionHashes(ids []string) map[string][32]byte {
	query := "SELECT opt_id, opt_hash FROM poll_option_id WHERE msg_mxid=$1 AND opt_id = ANY($2)"
	var args []any
	if msg.db.Dialect == dbutil.Postgres {
		args = []any{msg.MXID, pq.Array(ids)}
	} else {
		query = strings.ReplaceAll(query, " = ANY($2)", fmt.Sprintf(" IN (%s)", strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")))
		args = make([]any, len(ids)+1)
		args[0] = msg.MXID
		for i, id := range ids {
			args[i+1] = id
		}
	}
	hashes := make(map[string][32]byte, len(ids))
	rows, err := msg.db.Query(query, args...)
	if err != nil {
		msg.log.Errorfln("Failed to query poll option hashes for %s: %v", msg.MXID, err)
	} else {
		for rows.Next() {
			id, hash, err := scanPollOptionMapping(rows)
			if err != nil {
				msg.log.Errorfln("Failed to scan poll option hash for %s: %v", msg.MXID, err)
				break
			}
			hashes[id] = hash
		}
	}
	return hashes
}
