package wadb

import (
	"context"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

type PollOptionQuery struct {
	BridgeID networkid.BridgeID
	*dbutil.Database
}

type pollOption struct {
	id   string
	hash [32]byte
}

const (
	putPollOptionBaseQuery = `
		INSERT INTO whatsapp_poll_option_id (bridge_id, msg_mxid, opt_id, opt_hash)
		VALUES ($1, $2, $3, $4)
	`
	getPollOptionIDsByHashesQuery         = "SELECT opt_id, opt_hash FROM whatsapp_poll_option_id WHERE bridge_id=$1 AND msg_mxid=$2 AND opt_hash = ANY($3)"
	getPollOptionHashesByIDsQuery         = "SELECT opt_id, opt_hash FROM whatsapp_poll_option_id WHERE bridge_id=$1 AND msg_mxid=$2 AND opt_id = ANY($3)"
	getPollOptionQuerySQLiteArrayTemplate = " IN (%s)"
	getPollOptionQueryArrayPlaceholder    = " = ANY($3)"
)

func init() {
	if strings.ReplaceAll(getPollOptionIDsByHashesQuery, getPollOptionQueryArrayPlaceholder, "meow") == getPollOptionIDsByHashesQuery {
		panic("Array select query placeholder not found")
	}
	if strings.ReplaceAll(getPollOptionHashesByIDsQuery, getPollOptionQueryArrayPlaceholder, "meow") == getPollOptionIDsByHashesQuery {
		panic("Array select query placeholder not found")
	}
}

var pollOptionInserter = dbutil.NewMassInsertBuilder[*pollOption, [2]any](
	putPollOptionBaseQuery, "($1, $2, $%d, $%d)",
)

func (poq *PollOptionQuery) Put(ctx context.Context, mxid id.EventID, opts map[[32]byte]string) error {
	if len(opts) == 0 {
		return nil
	}
	optArray := make([]*pollOption, len(opts))
	i := 0
	for hash, optID := range opts {
		optArray[i] = &pollOption{id: optID, hash: hash}
		i++
	}
	query, args := pollOptionInserter.Build([2]any{poq.BridgeID, mxid}, optArray)
	_, err := poq.Exec(ctx, query, args...)
	return err
}

func (poq *PollOptionQuery) GetIDs(ctx context.Context, mxid id.EventID, hashes [][]byte) (map[[32]byte]string, error) {
	return getPollOptions(
		poq, ctx, mxid, getPollOptionIDsByHashesQuery, hashes,
		func(t *pollOption) ([32]byte, string) { return t.hash, t.id },
	)
}

func (poq *PollOptionQuery) GetHashes(ctx context.Context, mxid id.EventID, ids []string) (map[string][32]byte, error) {
	return getPollOptions(
		poq, ctx, mxid, getPollOptionHashesByIDsQuery, ids,
		func(t *pollOption) (string, [32]byte) { return t.id, t.hash },
	)
}

func getPollOptions[LookupKey any, Key comparable, Value any](
	poq *PollOptionQuery,
	ctx context.Context,
	mxid id.EventID,
	query string,
	things []LookupKey,
	getKeyValue func(option *pollOption) (Key, Value),
) (map[Key]Value, error) {
	var args []any
	if poq.Dialect == dbutil.Postgres {
		args = []any{poq.BridgeID, mxid, pq.Array(things)}
	} else {
		query = strings.ReplaceAll(query, getPollOptionQueryArrayPlaceholder, fmt.Sprintf(getPollOptionQuerySQLiteArrayTemplate, strings.TrimSuffix(strings.Repeat("?,", len(things)), ",")))
		args = make([]any, len(things)+2)
		args[0] = poq.BridgeID
		args[1] = mxid
		for i, thing := range things {
			args[i+2] = thing
		}
	}
	return dbutil.RowIterAsMap(
		dbutil.ConvertRowFn[*pollOption](scanPollOption).NewRowIter(poq.Query(ctx, query, args...)),
		getKeyValue,
	)
}

func scanPollOption(row dbutil.Scannable) (*pollOption, error) {
	var optionID string
	var hash []byte
	err := row.Scan(&optionID, &hash)
	if err != nil {
		return nil, err
	} else if len(hash) != 32 {
		return nil, fmt.Errorf("invalid hash length: %d", len(hash))
	}
	return &pollOption{
		id:   optionID,
		hash: [32]byte(hash),
	}, nil
}

func (po *pollOption) GetMassInsertValues() [2]any {
	return [2]any{po.id, po.hash[:]}
}
