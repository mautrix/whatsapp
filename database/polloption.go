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

	"github.com/lib/pq"

	"go.mau.fi/util/dbutil"
)

const (
	bulkPutPollOptionsQuery               = "INSERT INTO poll_option_id (msg_mxid, opt_id, opt_hash) VALUES ($1, $2, $3)"
	bulkPutPollOptionsQueryTemplate       = "($1, $%d, $%d)"
	bulkPutPollOptionsQueryPlaceholder    = "($1, $2, $3)"
	getPollOptionIDsByHashesQuery         = "SELECT opt_id, opt_hash FROM poll_option_id WHERE msg_mxid=$1 AND opt_hash = ANY($2)"
	getPollOptionHashesByIDsQuery         = "SELECT opt_id, opt_hash FROM poll_option_id WHERE msg_mxid=$1 AND opt_id = ANY($2)"
	getPollOptionQuerySQLiteArrayTemplate = " IN (%s)"
	getPollOptionQueryArrayPlaceholder    = " = ANY($2)"
)

func init() {
	if strings.ReplaceAll(bulkPutPollOptionsQuery, bulkPutPollOptionsQueryPlaceholder, "meow") == bulkPutPollOptionsQuery {
		panic("Bulk insert query placeholder not found")
	}
	if strings.ReplaceAll(getPollOptionIDsByHashesQuery, getPollOptionQueryArrayPlaceholder, "meow") == getPollOptionIDsByHashesQuery {
		panic("Array select query placeholder not found")
	}
	if strings.ReplaceAll(getPollOptionHashesByIDsQuery, getPollOptionQueryArrayPlaceholder, "meow") == getPollOptionIDsByHashesQuery {
		panic("Array select query placeholder not found")
	}
}

type pollOption struct {
	id   string
	hash [32]byte
}

func scanPollOption(rows dbutil.Scannable) (*pollOption, error) {
	var hash []byte
	var id string
	err := rows.Scan(&id, &hash)
	if err != nil {
		return nil, err
	} else if len(hash) != 32 {
		return nil, fmt.Errorf("unexpected hash length %d", len(hash))
	} else {
		return &pollOption{id: id, hash: [32]byte(hash)}, nil
	}
}

func (msg *Message) PutPollOptions(ctx context.Context, opts map[[32]byte]string) error {
	args := make([]any, len(opts)*2+1)
	placeholders := make([]string, len(opts))
	args[0] = msg.MXID
	i := 0
	for hash, id := range opts {
		args[i*2+1] = id
		hashCopy := hash
		args[i*2+2] = hashCopy[:]
		placeholders[i] = fmt.Sprintf(bulkPutPollOptionsQueryTemplate, i*2+2, i*2+3)
		i++
	}
	query := strings.ReplaceAll(bulkPutPollOptionsQuery, bulkPutPollOptionsQueryPlaceholder, strings.Join(placeholders, ","))
	return msg.qh.Exec(ctx, query, args...)
}

func getPollOptions[LookupKey any, Key comparable, Value any](
	ctx context.Context,
	msg *Message,
	query string,
	things []LookupKey,
	getKeyValue func(option *pollOption) (Key, Value),
) (map[Key]Value, error) {
	var args []any
	if msg.qh.GetDB().Dialect == dbutil.Postgres {
		args = []any{msg.MXID, pq.Array(things)}
	} else {
		query = strings.ReplaceAll(query, getPollOptionQueryArrayPlaceholder, fmt.Sprintf(getPollOptionQuerySQLiteArrayTemplate, strings.TrimSuffix(strings.Repeat("?,", len(things)), ",")))
		args = make([]any, len(things)+1)
		args[0] = msg.MXID
		for i, thing := range things {
			args[i+1] = thing
		}
	}
	return dbutil.RowIterAsMap(
		dbutil.ConvertRowFn[*pollOption](scanPollOption).NewRowIter(msg.qh.GetDB().Query(ctx, query, args...)),
		getKeyValue,
	)
}

func (msg *Message) GetPollOptionIDs(ctx context.Context, hashes [][]byte) (map[[32]byte]string, error) {
	return getPollOptions(
		ctx, msg, getPollOptionIDsByHashesQuery, hashes,
		func(t *pollOption) ([32]byte, string) { return t.hash, t.id },
	)
}

func (msg *Message) GetPollOptionHashes(ctx context.Context, ids []string) (map[string][32]byte, error) {
	return getPollOptions(
		ctx, msg, getPollOptionHashesByIDsQuery, ids,
		func(t *pollOption) (string, [32]byte) { return t.id, t.hash },
	)
}
