package wadb

import (
	"context"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/whatsmeow/types"
)

type AvatarCacheQuery struct {
	*dbutil.QueryHelper[*AvatarCacheEntry]
}

const (
	getAvatarCacheEntry = `
		SELECT entity_jid, avatar_id, direct_path, expiry, gone
		FROM whatsapp_avatar_cache
		WHERE entity_jid = $1 AND avatar_id = $2
	`
	putAvatarCacheEntry = `
		INSERT INTO whatsapp_avatar_cache (entity_jid, avatar_id, direct_path, expiry, gone)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (entity_jid, avatar_id) DO UPDATE
		SET direct_path = EXCLUDED.direct_path, expiry = EXCLUDED.expiry, gone = EXCLUDED.gone
	`
)

func (acq *AvatarCacheQuery) Get(ctx context.Context, entityJID types.JID, avatarID string) (*AvatarCacheEntry, error) {
	return acq.QueryOne(ctx, getAvatarCacheEntry, entityJID, avatarID)
}

func (acq *AvatarCacheQuery) Put(ctx context.Context, entry *AvatarCacheEntry) error {
	return acq.Exec(ctx, putAvatarCacheEntry, entry.sqlVariables()...)
}

type AvatarCacheEntry struct {
	EntityJID  types.JID
	AvatarID   string
	DirectPath string
	Expiry     jsontime.Unix
	Gone       bool
}

func (ace *AvatarCacheEntry) Scan(row dbutil.Scannable) (*AvatarCacheEntry, error) {
	return dbutil.ValueOrErr(ace, row.Scan(&ace.EntityJID, &ace.AvatarID, &ace.DirectPath, &ace.Expiry, &ace.Gone))
}

func (ace *AvatarCacheEntry) sqlVariables() []any {
	return []any{ace.EntityJID, ace.AvatarID, ace.DirectPath, ace.Expiry, ace.Gone}
}
